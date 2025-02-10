# cpaas-controller

This repository implements a cpaas controller for watching ControlPlane resources as
defined with a CustomResourceDefinition (CRD).

**Note:** go-get or vendor this package as `github.com/c-paas/cpaas-controller`.

The `update-codegen` script will automatically generate the following files &
directories:

* `pkg/apis/samplecontroller/v1alpha1/zz_generated.deepcopy.go`
* `pkg/generated/`

Changes should not be made to these files manually, and when creating your own
controller based off of this implementation you should not copy these files and
instead run the `update-codegen` script to generate your own.

## Fetch cpaas-controller and its dependencies

Issue the following commands --- starting in whatever working directory you like.

```sh
git clone https://github.com/c-paas/cpaas-controller
cd cpaas-controller
```

## Running

```sh
# to create and populate the `vendor` directory.
go mod vendor
# create working cluster (with kind for example)
kind create --config=kind.yaml cluster
# assumes you have a working kubeconfig, not required if operating in-cluster
make run
# create a CustomResourceDefinition
kubectl create -f artifacts/examples/crd-status-subresource.yaml
# create a custom resource of type ControlPlane
kubectl create -f artifacts/examples/example-cpaas.yaml
# check deployments created through the custom resource
kubectl get deployments
kubectl get controlplanes
```

## Cleanup

You can clean up the created CustomResourceDefinition with:
```sh
kubectl delete crd controlplanes.cpaascontroller.cpaas.io
```

Based on [sample-controller](https://github.com/kubernetes/sample-controller/tree/master)