/*
Copyright 2024.

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

package pagerdutycontrollers

import (
	"context"
	"fmt"

	"github.com/mattgialelis/dutycontroller/pkg/condtions"
	"github.com/mattgialelis/dutycontroller/pkg/providers/pd"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	pagerdutyv1beta1 "github.com/mattgialelis/dutycontroller/api/v1beta1"
)

const (
	businesServiceFinalizer = "businessservice.dutycontroller.io/finalizer"
)

// BusinessServiceReconciler reconciles a BusinessService object
type BusinessServiceReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	PagerClient *pd.Pagerduty
	recorder    record.EventRecorder
}

//+kubebuilder:rbac:groups=pagerduty.dutycontroller.io,resources=businessservices,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=pagerduty.dutycontroller.io,resources=businessservices/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=pagerduty.dutycontroller.io,resources=businessservices/finalizers,verbs=update

func (r *BusinessServiceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.WithValues("businessservice", req.NamespacedName)

	var businesService pagerdutyv1beta1.BusinessService

	// Fetch the BusinessService instance
	if err := r.Get(ctx, req.NamespacedName, &businesService); err != nil {
		//do not requeue if the resource does not exist
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get resource: %w", err)
	}

	defer func() {
		if businesService.DeletionTimestamp.IsZero() {
			if err := r.Status().Update(ctx, &businesService); err != nil {
				log.Error(err, "unable to update Business Service status")
			}
		}
	}()

	// Get Conditions
	// We do this here so we can use the condtions status in the rest of the function
	createdCondition := condtions.GetCondition(businesService.Status.Conditions, condtions.ConditionReasonCreated)

	// Check if the BusinessService instance is marked for deletion
	if businesService.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(&businesService, businesServiceFinalizer) {

			controllerutil.AddFinalizer(&businesService, businesServiceFinalizer)
			if err := r.Update(ctx, &businesService); err != nil {
				return ctrl.Result{}, fmt.Errorf("could not update finalizers: %w", err)
			}

			return ctrl.Result{}, nil
		}
	} else {
		if controllerutil.ContainsFinalizer(&businesService, businesServiceFinalizer) {
			// Run finalization logic for BusinessService
			// If the finalization logic fails, don't remove the finalizer so
			// that we can retry during the next reconciliation.

			if createdCondition != nil && createdCondition.Status == metav1.ConditionTrue {
				log.Info("Deleting BusinessService", "ID", businesService.Status.ID, "Name", businesService.Name)
				err := r.PagerClient.DeleteBusinessService(businesService.Status.ID)
				if err != nil {
					return ctrl.Result{}, fmt.Errorf("could not delete business service: %w", err)
				}
			}

			// Remove the finalizer
			controllerutil.RemoveFinalizer(&businesService, businesServiceFinalizer)
			if err := r.Client.Update(ctx, &businesService, &client.UpdateOptions{}); err != nil {
				return ctrl.Result{}, fmt.Errorf("could not update finalizers: %w", err)
			}

			return ctrl.Result{}, nil
		}
	}

	// Check if the BusinessService instance exists
	_, exists, err := r.PagerClient.GetBusinessServicebyName(ctx, req.Name, true)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("could not get business service by name: %w", err)
	}

	// Get the team ID
	teamId, _, err := r.PagerClient.GetTeambyName(businesService.Spec.Team)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("could not get team by name: %w", err)
	}

	//Convert the BusinessService CRD to a BusinessService struct
	pagerbusinesService := pd.BusinessServiceSpectoBusinessService(businesService, teamId)

	if !exists {
		id, err := r.PagerClient.CreateBusinessService(pagerbusinesService)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("could not create business service: %w", err)
		}

		if createdCondition == nil || createdCondition.Status != metav1.ConditionTrue {
			condtions.SetCondition(&businesService.Status.Conditions, condtions.ConditionReasonCreated, metav1.ConditionTrue, "Business Service created successfully in PagerDuty")
		}

		businesService.Status.ID = id
		log.Info("Created BusinessService", "ID", id, "Name", businesService.Name)
		return ctrl.Result{}, nil
	} else {

		if createdCondition != nil && createdCondition.Status == metav1.ConditionTrue {

			err = r.PagerClient.UpdateBusinessService(ctx, pagerbusinesService)
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("could not update business service: %w", err)
			}
			log.Info("Updated BusinessService", "ID", businesService.Status.ID, "Name", businesService.Name)

		} else {
			log.Info("Business Service already exists in Pagerduty", "ID", businesService.Status.ID)
			condtions.SetCondition(&businesService.Status.Conditions, condtions.ConditionReasonFailed, metav1.ConditionTrue, "BusinessService cannot be created, already exists in PagerDuty")
			return ctrl.Result{}, nil
		}

	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *BusinessServiceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.recorder = mgr.GetEventRecorderFor("businessservice")

	return ctrl.NewControllerManagedBy(mgr).
		For(&pagerdutyv1beta1.BusinessService{}).
		Complete(r)
}
