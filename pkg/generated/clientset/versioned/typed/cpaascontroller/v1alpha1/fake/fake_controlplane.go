/*
Copyright The Kubernetes Authors.

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

// Code generated by client-gen. DO NOT EDIT.

package fake

import (
	v1alpha1 "github.com/c-paas/cpaas-controller/pkg/apis/cpaascontroller/v1alpha1"
	cpaascontrollerv1alpha1 "github.com/c-paas/cpaas-controller/pkg/generated/clientset/versioned/typed/cpaascontroller/v1alpha1"
	gentype "k8s.io/client-go/gentype"
)

// fakeControlPlanes implements ControlPlaneInterface
type fakeControlPlanes struct {
	*gentype.FakeClientWithList[*v1alpha1.ControlPlane, *v1alpha1.ControlPlaneList]
	Fake *FakeCpaascontrollerV1alpha1
}

func newFakeControlPlanes(fake *FakeCpaascontrollerV1alpha1, namespace string) cpaascontrollerv1alpha1.ControlPlaneInterface {
	return &fakeControlPlanes{
		gentype.NewFakeClientWithList[*v1alpha1.ControlPlane, *v1alpha1.ControlPlaneList](
			fake.Fake,
			namespace,
			v1alpha1.SchemeGroupVersion.WithResource("controlplanes"),
			v1alpha1.SchemeGroupVersion.WithKind("ControlPlane"),
			func() *v1alpha1.ControlPlane { return &v1alpha1.ControlPlane{} },
			func() *v1alpha1.ControlPlaneList { return &v1alpha1.ControlPlaneList{} },
			func(dst, src *v1alpha1.ControlPlaneList) { dst.ListMeta = src.ListMeta },
			func(list *v1alpha1.ControlPlaneList) []*v1alpha1.ControlPlane {
				return gentype.ToPointerSlice(list.Items)
			},
			func(list *v1alpha1.ControlPlaneList, items []*v1alpha1.ControlPlane) {
				list.Items = gentype.FromPointerSlice(items)
			},
		),
		fake,
	}
}
