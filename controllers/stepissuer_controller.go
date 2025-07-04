/*

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

package controllers

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	api "github.com/njunot/step-issuer/api/v1beta1"
	"github.com/njunot/step-issuer/provisioners"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// StepIssuerReconciler reconciles a StepIssuer object
type StepIssuerReconciler struct {
	client.Client
	Log      logr.Logger
	Clock    clock.Clock
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=certmanager.step.sm,resources=stepissuers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=certmanager.step.sm,resources=stepissuers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;create;update

// Reconcile will read and validate the StepIssuer resources, it will set the
// status condition ready to true if everything is right.
func (r *StepIssuerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("stepissuer", req.NamespacedName)

	iss := new(api.StepIssuer)
	if err := r.Client.Get(ctx, req.NamespacedName, iss); err != nil {
		log.Error(err, "failed to retrieve StepIssuer resource")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	statusReconciler := newStepStatusReconciler(r, iss, log)
	if err := validateStepIssuerSpec(iss.Spec); err != nil {
		log.Error(err, "failed to validate StepIssuer resource")
		statusReconciler.UpdateNoError(ctx, api.ConditionFalse, "Validation", "Failed to validate resource: %v", err)
		return ctrl.Result{}, err
	}

	// Fetch the provisioner password
	var secret core.Secret
	secretNamespaceName := types.NamespacedName{
		Namespace: req.Namespace,
		Name:      iss.Spec.Provisioner.PasswordRef.Name,
	}
	if err := r.Client.Get(ctx, secretNamespaceName, &secret); err != nil {
		log.Error(err, "failed to retrieve StepIssuer provisioner secret", "namespace", secretNamespaceName.Namespace, "name", secretNamespaceName.Name)
		if apierrors.IsNotFound(err) {
			statusReconciler.UpdateNoError(ctx, api.ConditionFalse, "NotFound", "Failed to retrieve provisioner secret: %v", err)
		} else {
			statusReconciler.UpdateNoError(ctx, api.ConditionFalse, "Error", "Failed to retrieve provisioner secret: %v", err)
		}
		return ctrl.Result{}, err
	}
	password, ok := secret.Data[iss.Spec.Provisioner.PasswordRef.Key]
	if !ok {
		err := fmt.Errorf("secret %s does not contain key %s", secret.Name, iss.Spec.Provisioner.PasswordRef.Key)
		log.Error(err, "failed to retrieve StepIssuer provisioner secret", "namespace", secretNamespaceName.Namespace, "name", secretNamespaceName.Name)
		statusReconciler.UpdateNoError(ctx, api.ConditionFalse, "NotFound", "Failed to retrieve provisioner secret: %v", err)
		return ctrl.Result{}, err
	}

	// Initialize and store the provisioner
	p, err := provisioners.NewFromStepIssuer(iss, password)
	if err != nil {
		log.Error(err, "failed to initialize provisioner")
		statusReconciler.UpdateNoError(ctx, api.ConditionFalse, "Error", "failed initialize provisioner")
		return ctrl.Result{}, err
	}
	provisioners.Store(req.NamespacedName, p)

	return ctrl.Result{}, statusReconciler.Update(ctx, api.ConditionTrue, "Verified", "StepIssuer verified and ready to sign certificates")
}

// SetupWithManager initializes the StepIssuer controller into the controller
// runtime.
func (r *StepIssuerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&api.StepIssuer{}).
		Complete(r)
}

func validateStepIssuerSpec(s api.StepIssuerSpec) error {
	switch {
	case s.URL == "":
		return fmt.Errorf("spec.url cannot be empty")
	case len(s.CABundle) == 0:
		return fmt.Errorf("spec.caBundle cannot be empty")
	case s.Provisioner.Name == "":
		return fmt.Errorf("spec.provisioner.name cannot be empty")
	case s.Provisioner.KeyID == "":
		return fmt.Errorf("spec.provisioner.kid cannot be empty")
	case s.Provisioner.PasswordRef.Name == "":
		return fmt.Errorf("spec.provisioner.passwordRef.name cannot be empty")
	case s.Provisioner.PasswordRef.Key == "":
		return fmt.Errorf("spec.provisioner.passwordRef.key cannot be empty")
	default:
		return nil
	}
}
