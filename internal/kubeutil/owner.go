package kubeutil

import (
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// SetControllerOwner sets the owner reference for a child object.
func SetControllerOwner(owner client.Object, scheme *runtime.Scheme, obj client.Object) error {
	return controllerutil.SetControllerReference(owner, obj, scheme)
}
