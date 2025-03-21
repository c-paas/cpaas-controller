/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/time/rate"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	coreinformer "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	cpaasv1alpha1 "github.com/c-paas/cpaas-controller/pkg/apis/cpaascontroller/v1alpha1"
	cpaasscheme "github.com/c-paas/cpaas-controller/pkg/generated/clientset/versioned/scheme"

	clientset "github.com/c-paas/cpaas-controller/pkg/generated/clientset/versioned"
	informers "github.com/c-paas/cpaas-controller/pkg/generated/informers/externalversions/cpaascontroller/v1alpha1"
	listers "github.com/c-paas/cpaas-controller/pkg/generated/listers/cpaascontroller/v1alpha1"
)

const controllerAgentName = "cpaas-controller"

const (
	// SuccessSynced is used as part of the Event 'reason' when a ControlPlane is synced
	SuccessSynced = "Synced"
	// ErrResourceExists is used as part of the Event 'reason' when a ControlPlane fails
	// to sync due to a Deployment of the same name already existing.
	ErrResourceExists = "ErrResourceExists"

	// MessageResourceExists is the message used for Events when a resource
	// fails to sync due to a Deployment already existing
	MessageResourceExists = "Resource %q already exists and is not managed by ControlPlane"
	// MessageResourceSynced is the message used for an Event fired when a ControlPlane
	// is synced successfully
	MessageResourceSynced = "ControlPlane synced successfully"
	// FieldManager distinguishes this controller from other things writing to API objects
	FieldManager = controllerAgentName
)

// Controller is the controller implementation for ControlPlane resources
type Controller struct {
	// kubeclientset is a standard kubernetes clientset
	kubeclientset kubernetes.Interface
	// clientset is a clientset for our own API group
	clientset clientset.Interface

	podsLister          corelisters.PodLister
	podsSynced          cache.InformerSynced
	controlPlanesLister listers.ControlPlaneLister
	controlPlanesSynced cache.InformerSynced

	workqueue workqueue.TypedRateLimitingInterface[cache.ObjectName]
	recorder  record.EventRecorder
}

// NewController returns a new sample controller
func NewController(
	ctx context.Context,
	kubeclientset kubernetes.Interface,
	clientset clientset.Interface,
	podsInformer coreinformer.PodInformer,
	informer informers.ControlPlaneInformer,
) *Controller {
	logger := klog.FromContext(ctx)

	utilruntime.Must(cpaasscheme.AddToScheme(scheme.Scheme))
	logger.V(4).Info("Creating event broadcaster")

	eventBroadcaster := record.NewBroadcaster(record.WithContext(ctx))
	eventBroadcaster.StartStructuredLogging(0)
	eventBroadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: kubeclientset.CoreV1().Events("")})
	recorder := eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: controllerAgentName})
	ratelimiter := workqueue.NewTypedMaxOfRateLimiter(
		workqueue.NewTypedItemExponentialFailureRateLimiter[cache.ObjectName](5*time.Millisecond, 1000*time.Second),
		&workqueue.TypedBucketRateLimiter[cache.ObjectName]{Limiter: rate.NewLimiter(rate.Limit(50), 300)},
	)

	controller := &Controller{
		kubeclientset:       kubeclientset,
		clientset:           clientset,
		podsLister:          podsInformer.Lister(),
		podsSynced:          podsInformer.Informer().HasSynced,
		controlPlanesLister: informer.Lister(),
		controlPlanesSynced: informer.Informer().HasSynced,
		workqueue:           workqueue.NewTypedRateLimitingQueue(ratelimiter),
		recorder:            recorder,
	}

	logger.Info("Setting up event handlers")
	// Set up an event handler for when ControlPlane resources change
	informer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.enqueueControlPlane,
		UpdateFunc: func(old, new interface{}) {
			controller.enqueueControlPlane(new)
		},
	})
	// Set up an event handler for when Deployment resources change. This
	// handler will lookup the owner of the given Deployment, and if it is
	// owned by a ControlPlane resource then the handler will enqueue that ControlPlane resource for
	// processing. This way, we don't need to implement custom logic for
	// handling Deployment resources. More info on this pattern:
	// https://github.com/kubernetes/community/blob/8cafef897a22026d42f5e5bb3f104febe7e29830/contributors/devel/controllers.md
	podsInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.handleObject,
		UpdateFunc: func(old, new interface{}) {
			newPod := new.(*corev1.Pod)
			oldPod := old.(*corev1.Pod)
			if newPod.ResourceVersion == oldPod.ResourceVersion {
				return
			}
			controller.handleObject(new)
		},
		DeleteFunc: controller.handleObject,
	})

	return controller
}

// Run will set up the event handlers for types we are interested in, as well
// as syncing informer caches and starting workers. It will block until stopCh
// is closed, at which point it will shutdown the workqueue and wait for
// workers to finish processing their current work items.
func (c *Controller) Run(ctx context.Context, workers int) error {
	defer utilruntime.HandleCrash()
	defer c.workqueue.ShutDown()
	logger := klog.FromContext(ctx)

	// Start the informer factories to begin populating the informer caches
	logger.Info("Starting ControlPlane controller")

	// Wait for the caches to be synced before starting workers
	logger.Info("Waiting for informer caches to sync")

	if ok := cache.WaitForCacheSync(ctx.Done(), c.podsSynced, c.controlPlanesSynced); !ok {
		return fmt.Errorf("failed to wait for caches to sync")
	}

	logger.Info("Starting workers", "count", workers)
	// Launch two workers to process ControlPlane resources
	for i := 0; i < workers; i++ {
		go wait.UntilWithContext(ctx, c.runWorker, time.Second)
	}

	logger.Info("Started workers")
	<-ctx.Done()
	logger.Info("Shutting down workers")

	return nil
}

// runWorker is a long-running function that will continually call the
// processNextWorkItem function in order to read and process a message on the
// workqueue.
func (c *Controller) runWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

// processNextWorkItem will read a single work item off the workqueue and
// attempt to process it, by calling the syncHandler.
func (c *Controller) processNextWorkItem(ctx context.Context) bool {
	objRef, shutdown := c.workqueue.Get()
	logger := klog.FromContext(ctx)

	if shutdown {
		return false
	}

	// We call Done at the end of this func so the workqueue knows we have
	// finished processing this item. We also must remember to call Forget
	// if we do not want this work item being re-queued. For example, we do
	// not call Forget if a transient error occurs, instead the item is
	// put back on the workqueue and attempted again after a back-off
	// period.
	defer c.workqueue.Done(objRef)

	// Run the syncHandler, passing it the structured reference to the object to be synced.
	err := c.syncHandler(ctx, objRef)
	if err == nil {
		// If no error occurs then we Forget this item so it does not
		// get queued again until another change happens.
		c.workqueue.Forget(objRef)
		logger.Info("Successfully synced", "objectName", objRef)
		return true
	}
	// there was a failure so be sure to report it.  This method allows for
	// pluggable error handling which can be used for things like
	// cluster-monitoring.
	utilruntime.HandleErrorWithContext(ctx, err, "Error syncing; requeuing for later retry", "objectReference", objRef)
	// since we failed, we should requeue the item to work on later.  This
	// method will add a backoff to avoid hotlooping on particular items
	// (they're probably still not going to work right away) and overall
	// controller protection (everything I've done is broken, this controller
	// needs to calm down or it can starve other useful work) cases.
	c.workqueue.AddRateLimited(objRef)
	return true
}

// syncHandler compares the actual state with the desired, and attempts to
// converge the two. It then updates the Status block of the Foo resource
// with the current status of the resource.
func (c *Controller) syncHandler(ctx context.Context, objectRef cache.ObjectName) error {
	logger := klog.LoggerWithValues(klog.FromContext(ctx), "objectRef", objectRef)

	// Get the ControlPlane resource with this namespace/name
	cp, err := c.controlPlanesLister.ControlPlanes(objectRef.Namespace).Get(objectRef.Name)
	if err != nil {
		// The ControlPlane resource may no longer exist, in which case we stop
		// processing.
		if errors.IsNotFound(err) {
			utilruntime.HandleErrorWithContext(ctx, err, "ControlPlane referenced by item in work queue no longer exists", "objectReference", objectRef)
			return nil
		}

		return err
	}

	cpname := cp.Spec.Name
	if cpname == "" {
		// We choose to absorb the error here as the worker would requeue the
		// resource otherwise. Instead, the next time the resource is updated
		// the resource will be queued again.
		utilruntime.HandleErrorWithContext(ctx, nil, "Deployment name missing from object reference", "objectReference", objectRef)
		return nil
	}

	// Get the pods from control plane
	selector := labels.NewSelector()
	// TODO log error
	podReq, _ := labels.NewRequirement("controller", selection.Equals, []string{cp.Name})
	selector = selector.Add(*podReq)
	pods, err := c.podsLister.Pods(cp.Namespace).List(selector)
	// If the resource doesn't exist, we'll create it
	if len(pods) == 0 {
		for _, p := range buildPods(cp) {
			_, err := c.kubeclientset.CoreV1().Pods(cp.Namespace).Create(ctx, &p, metav1.CreateOptions{FieldManager: FieldManager})
			if err != nil {
				logger.V(4).Error(err, "cant create pod", "pod", p)
				return err
			}
		}
		// pods, err = c.kubeclientset.CoreV1().Pods(cp.Namespace).Create(ctx, newDeployment(cp), metav1.CreateOptions{FieldManager: FieldManager})
	}
	// If an error occurs during Get/Create, we'll requeue the item so we can
	// attempt processing again later. This could have been caused by a
	// temporary network failure, or any other transient reason.
	if err != nil {
		return err
	}
	logger.V(4).Info("Found pods", "pods", pods)

	// If the Pods are not controlled by this ControlPlane resource, we should log
	// a warning to the event recorder and return error msg.
	for _, p := range pods {
		if !metav1.IsControlledBy(p, cp) {
			msg := fmt.Sprintf(MessageResourceExists, cp.Name)
			c.recorder.Event(cp, corev1.EventTypeWarning, ErrResourceExists, msg)
			return fmt.Errorf("%s", msg)
		}
	}

	// If this number of the replicas on the ControlPlane resource is specified, and the
	// number does not equal the current desired replicas on the Deployment, we
	// should update the Deployment resource.
	// TODO check if all pods are healthy and running
	// if cp.Spec.Replicas != nil && *cp.Spec.Replicas != *deployment.Spec.Replicas {
	// 	logger.V(4).Info("Update deployment resource", "currentReplicas", *deployment.Spec.Replicas, "desiredReplicas", *cp.Spec.Replicas)
	// 	deployment, err = c.kubeclientset.AppsV1().Deployments(cp.Namespace).Update(ctx, newDeployment(cp), metav1.UpdateOptions{FieldManager: FieldManager})
	// }

	// If an error occurs during Update, we'll requeue the item so we can
	// attempt processing again later. This could have been caused by a
	// temporary network failure, or any other transient reason.
	// if err != nil {
	// 	return err
	// }

	// Finally, we update the status block of the ControlPlane resource to reflect the
	// current state of the world
	for _, p := range pods {
		err = c.updateControlPlaneStatus(ctx, cp, p)
		if err != nil {
			return err
		}
	}

	c.recorder.Event(cp, corev1.EventTypeNormal, SuccessSynced, MessageResourceSynced)
	return nil
}

// TODO mock pods status for a while
func (c *Controller) updateControlPlaneStatus(ctx context.Context, cp *cpaasv1alpha1.ControlPlane, _ *corev1.Pod) error {
	// NEVER modify objects from the store. It's a read-only, local cache.
	// You can use DeepCopy() to make a deep copy of original object and modify this copy
	// Or create a copy manually for better performance
	cpCopy := cp.DeepCopy()
	cpCopy.Status.AllPodsRunning = true
	// If the CustomResourceSubresources feature gate is not enabled,
	// we must use Update instead of UpdateStatus to update the Status block of the ControlPlane resource.
	// UpdateStatus will not allow changes to the Spec of the resource,
	// which is ideal for ensuring nothing other than resource status has been updated.
	_, err := c.clientset.CpaascontrollerV1alpha1().ControlPlanes(cp.Namespace).UpdateStatus(ctx, cpCopy, metav1.UpdateOptions{FieldManager: FieldManager})
	return err
}

// enqueueControlPlane takes ControlPlane resource and converts it into a namespace/name
// string which is then put onto the work queue. This method should *not* be
// passed resources of any type other than ControlPlane.
func (c *Controller) enqueueControlPlane(obj interface{}) {
	if objectRef, err := cache.ObjectToName(obj); err != nil {
		utilruntime.HandleError(err)
		return
	} else {
		c.workqueue.Add(objectRef)
	}
}

// handleObject will take any resource implementing metav1.Object and attempt
// to find the ControlPlane resource that 'owns' it. It does this by looking at the
// objects metadata.ownerReferences field for an appropriate OwnerReference.
// It then enqueues that ControlPlane resource to be processed. If the object does not
// have an appropriate OwnerReference, it will simply be skipped.
func (c *Controller) handleObject(obj interface{}) {
	var object metav1.Object
	var ok bool
	logger := klog.FromContext(context.Background())
	if object, ok = obj.(metav1.Object); !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			// If the object value is not too big and does not contain sensitive information then
			// it may be useful to include it.
			utilruntime.HandleErrorWithContext(context.Background(), nil, "Error decoding object, invalid type", "type", fmt.Sprintf("%T", obj))
			return
		}
		object, ok = tombstone.Obj.(metav1.Object)
		if !ok {
			// If the object value is not too big and does not contain sensitive information then
			// it may be useful to include it.
			utilruntime.HandleErrorWithContext(context.Background(), nil, "Error decoding object tombstone, invalid type", "type", fmt.Sprintf("%T", tombstone.Obj))
			return
		}
		logger.V(4).Info("Recovered deleted object", "resourceName", object.GetName())
	}
	logger.V(4).Info("Processing object", "object", klog.KObj(object))
	if ownerRef := metav1.GetControllerOf(object); ownerRef != nil {
		// If this object is not owned by a ControlPlane, we should not do anything more
		// with it.
		if ownerRef.Kind != "ControlPlane" {
			return
		}

		cp, err := c.controlPlanesLister.ControlPlanes(object.GetNamespace()).Get(ownerRef.Name)
		if err != nil {
			logger.V(4).Info("Ignore orphaned object", "object", klog.KObj(object), "controlplane", ownerRef.Name)
			return
		}

		c.enqueueControlPlane(cp)
		return
	}
}

// buildPods creates a bunch of pods for a ControlPlane resource. It also sets
// the appropriate OwnerReferences on the resource so handleObject can discover
// the ControlPlane resource that 'owns' it.
func buildPods(cp *cpaasv1alpha1.ControlPlane) []corev1.Pod {
	return []corev1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "etcd",
				Namespace: cp.Namespace,
				OwnerReferences: []metav1.OwnerReference{
					*metav1.NewControllerRef(cp, cpaasv1alpha1.SchemeGroupVersion.WithKind("ControlPlane")),
				},
				Labels: map[string]string{
					"app":        "etcd",
					"controller": cp.Name,
				},
			},
			Spec: corev1.PodSpec{
				Volumes: []corev1.Volume{
					{
						Name: "certs",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "certs",
								},
							},
						},
					},
				},
				Containers: []corev1.Container{
					{
						Name:  "etcd",
						Image: "registry.k8s.io/etcd:3.5.16-0",
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      "certs",
								ReadOnly:  true,
								MountPath: "/etc/kubernetes/pki/certs",
							},
						},
						Ports: []corev1.ContainerPort{
							{
								Name:          "one",
								ContainerPort: 2379,
								Protocol:      corev1.ProtocolTCP,
							},
							{
								Name:          "two",
								ContainerPort: 2380,
								Protocol:      corev1.ProtocolTCP,
							},
							{
								Name:          "three",
								ContainerPort: 2381,
								Protocol:      corev1.ProtocolTCP,
							},
						},
						Command: []string{
							"etcd",
							"--advertise-client-urls=https://0.0.0.0:2379",
							"--initial-advertise-peer-urls=https://0.0.0.0:2380",
							"--initial-cluster=control-plane=https://0.0.0.0:2380",
							"--cert-file=/etc/kubernetes/pki/certs/kube-api-server.crt",
							"--data-dir=/var/lib/etcd",
							"--experimental-initial-corrupt-check=true",
							"--experimental-watch-progress-notify-interval=5s",
							"--listen-client-urls=https://0.0.0.0:2379",
							"--listen-metrics-urls=http://0.0.0.0:2381",
							"--listen-peer-urls=https://0.0.0.0:2380",
							"--name=control-plane",
							"--client-cert-auth=true",
							"--key-file=/etc/kubernetes/pki/certs/kube-api-server.key",
							"--peer-cert-file=/etc/kubernetes/pki/certs/kube-api-server.crt",
							"--peer-key-file=/etc/kubernetes/pki/certs/kube-api-server.key",
							"--peer-client-cert-auth=true",
							"--peer-trusted-ca-file=/etc/kubernetes/pki/certs/ca.crt",
							"--trusted-ca-file=/etc/kubernetes/pki/certs/ca.crt",
							"--snapshot-count=10000",
						},
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "api-server",
				Namespace: cp.Namespace,
				OwnerReferences: []metav1.OwnerReference{
					*metav1.NewControllerRef(cp, cpaasv1alpha1.SchemeGroupVersion.WithKind("ControlPlane")),
				},
				Labels: map[string]string{
					"app":        "api-server",
					"controller": cp.Name,
				},
			},
			Spec: corev1.PodSpec{
				Volumes: []corev1.Volume{
					{
						Name: "certs",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "certs",
								},
							},
						},
					},
				},
				Containers: []corev1.Container{
					{
						Name:  "api-server",
						Image: "rancher/hyperkube:v1.31.5-rancher1",
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      "certs",
								ReadOnly:  true,
								MountPath: "/etc/kubernetes/pki/certs",
							},
						},
						Ports: []corev1.ContainerPort{
							{
								Name:          "https",
								ContainerPort: 6443,
								Protocol:      corev1.ProtocolTCP,
							},
						},
						Command: []string{
							"/usr/local/bin/kube-apiserver",
							"--service-account-key-file=/etc/kubernetes/pki/certs/kube-api-server.crt",
							"--service-account-signing-key-file=/etc/kubernetes/pki/certs/kube-api-server.key",
							"--service-account-issuer=api",
							"--bind-address=0.0.0.0",
							"--authorization-mode=Node,RBAC",
							"--etcd-servers=https://etcd:2379",
							"--etcd-cafile=/etc/kubernetes/pki/certs/ca.crt",
							"--etcd-certfile=/etc/kubernetes/pki/certs/kube-api-server.crt",
							"--etcd-keyfile=/etc/kubernetes/pki/certs/kube-api-server.key",
							"--service-cluster-ip-range=10.0.0.0/16",
						},
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "kube-controller-manager",
				Namespace: cp.Namespace,
				OwnerReferences: []metav1.OwnerReference{
					*metav1.NewControllerRef(cp, cpaasv1alpha1.SchemeGroupVersion.WithKind("ControlPlane")),
				},
				Labels: map[string]string{
					"app":        "kube-controller-manager",
					"controller": cp.Name,
				},
			},
			Spec: corev1.PodSpec{
				Volumes: []corev1.Volume{
					{
						Name: "certs",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "certs",
								},
							},
						},
					},
				},
				Containers: []corev1.Container{
					{
						Name:  "kube-controller-manager",
						Image: "rancher/hyperkube:v1.31.5-rancher1",
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      "certs",
								ReadOnly:  true,
								MountPath: "/etc/kubernetes/pki/certs",
							},
						},
						Command: []string{
							"/usr/local/bin/kube-controller-manager",
							"--cluster-cidr=10.10.0.0/16",
							"--master=https://api-server:6443",
							"--tls-cert-file=/etc/kubernetes/pki/certs/kube-controller-manager.crt",
							"--tls-private-key-file=/etc/kubernetes/pki/certs/kube-controller-manager.key",
							"--service-cluster-ip-range=10.0.0.0/16",
							"--leader-elect=false",
						},
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "kube-scheduler",
				Namespace: cp.Namespace,
				OwnerReferences: []metav1.OwnerReference{
					*metav1.NewControllerRef(cp, cpaasv1alpha1.SchemeGroupVersion.WithKind("ControlPlane")),
				},
				Labels: map[string]string{
					"app":        "kube-scheduler",
					"controller": cp.Name,
				},
			},
			Spec: corev1.PodSpec{
				Volumes: []corev1.Volume{
					{
						Name: "certs",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "certs",
								},
							},
						},
					},
				},
				Containers: []corev1.Container{
					{
						Name:  "kube-scheduler",
						Image: "rancher/hyperkube:v1.31.5-rancher1",
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      "certs",
								ReadOnly:  true,
								MountPath: "/etc/kubernetes/pki/certs",
							},
						},
						Command: []string{
							"/usr/local/bin/kube-scheduler",
							"--tls-cert-file=/etc/kubernetes/pki/certs/kube-scheduler.crt",
							"--tls-private-key-file=/etc/kubernetes/pki/certs/kube-scheduler.key",
							"--master=https://api-server:6443",
						},
					},
				},
			},
		},
	}
}
