package controllers

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr" // Required for ServicePort TargetPort
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	webappv1 "github.com/your-org/my-app-controller/api/v1" // Make sure this path is correct based on your init command
)

// AppReconciler reconciles an App object
type AppReconciler struct {
	client.Client                 // Client provides methods to interact with the Kubernetes API server.
	Scheme        *runtime.Scheme // Scheme contains the Go type definitions for all API kinds that this controller works with.
}

//+kubebuilder:rbac:groups=webapp.example.com,resources=apps,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=webapp.example.com,resources=apps/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=webapp.example.com,resources=apps/finalizers,verbs=update
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch

// Reconcile is the main reconciliation loop. It fetches the App object and ensures
// that the corresponding Deployment and Service exist and match the desired state.
func (r *AppReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// Use a logger for structured logging.
	log := log.FromContext(ctx)

	// 1. Fetch the App instance that triggered this reconciliation.
	app := &webappv1.App{}
	err := r.Get(ctx, req.NamespacedName, app)
	if err != nil {
		if errors.IsNotFound(err) {
			// App object not found. This means the object has been deleted from the cluster.
			// We can stop reconciling and return. Owned objects (Deployment, Service)
			// will be garbage collected automatically due to owner references.
			log.Info("App resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		// Error reading the object. Requeue the request to retry later.
		log.Error(err, "Failed to get App")
		return ctrl.Result{}, err
	}

	// 2. Define the desired state for the Deployment based on the App's spec.
	desiredDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-deployment", app.Name), // Name the deployment based on the App's name
			Namespace: app.Namespace,
			Labels: map[string]string{
				"app":        app.Name,
				"controller": "app-controller",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &app.Spec.Replicas, // Set replicas from AppSpec
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": app.Name, // Selector to match pods created by this deployment
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": app.Name,
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "app-container",
						Image: app.Spec.Image, // Use image from AppSpec
						Ports: []corev1.ContainerPort{{
							ContainerPort: app.Spec.Port, // Expose port from AppSpec
						}},
					}},
				},
			},
		},
	}

	// 3. Set the App instance as the owner of the Deployment.
	// This is crucial for Kubernetes' garbage collection. When the App is deleted,
	// this owned Deployment will automatically be deleted too.
	if err := ctrl.SetControllerReference(app, desiredDeployment, r.Scheme); err != nil {
		log.Error(err, "Failed to set controller reference for Deployment")
		return ctrl.Result{}, err
	}

	// 4. Check if the Deployment already exists.
	foundDeployment := &appsv1.Deployment{}
	err = r.Get(ctx, types.NamespacedName{Name: desiredDeployment.Name, Namespace: desiredDeployment.Namespace}, foundDeployment)
	if err != nil && errors.IsNotFound(err) {
		// Deployment does not exist, so create it.
		log.Info("Creating a new Deployment", "Deployment.Namespace", desiredDeployment.Namespace, "Deployment.Name", desiredDeployment.Name)
		err = r.Create(ctx, desiredDeployment)
		if err != nil {
			log.Error(err, "Failed to create new Deployment", "Deployment.Namespace", desiredDeployment.Namespace, "Deployment.Name", desiredDeployment.Name)
			return ctrl.Result{}, err
		}
		// Deployment created successfully.
	} else if err != nil {
		// Error getting the Deployment. Requeue.
		log.Error(err, "Failed to get Deployment")
		return ctrl.Result{}, err
	} else {
		// Deployment found. Check if an update is needed.
		if !deploymentEqual(foundDeployment.Spec, desiredDeployment.Spec) {
			log.Info("Updating existing Deployment", "Deployment.Namespace", foundDeployment.Namespace, "Deployment.Name", foundDeployment.Name)
			// Copy the desired spec to the found deployment object.
			foundDeployment.Spec = desiredDeployment.Spec
			err = r.Update(ctx, foundDeployment)
			if err != nil {
				log.Error(err, "Failed to update Deployment", "Deployment.Namespace", foundDeployment.Namespace, "Deployment.Name", foundDeployment.Name)
				return ctrl.Result{}, err
			}
		} else {
			log.V(1).Info("Deployment is up-to-date", "Deployment.Namespace", foundDeployment.Namespace, "Deployment.Name", foundDeployment.Name)
		}
	}

	// 5. Define the desired state for the Service based on the App's spec.
	desiredService := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-service", app.Name), // Name the service based on the App's name
			Namespace: app.Namespace,
			Labels: map[string]string{
				"app":        app.Name,
				"controller": "app-controller",
			},
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{
				"app": app.Name, // Selector to match pods created by the deployment
			},
			Ports: []corev1.ServicePort{{
				Protocol:   corev1.ProtocolTCP,
				Port:       app.Spec.Port,
				TargetPort: intstr.FromInt(int(app.Spec.Port)), // Target the container port
			}},
			Type: corev1.ServiceTypeClusterIP, // Expose service internally
		},
	}

	// 6. Set the App instance as the owner of the Service.
	if err := ctrl.SetControllerReference(app, desiredService, r.Scheme); err != nil {
		log.Error(err, "Failed to set controller reference for Service")
		return ctrl.Result{}, err
	}

	// 7. Check if the Service already exists.
	foundService := &corev1.Service{}
	err = r.Get(ctx, types.NamespacedName{Name: desiredService.Name, Namespace: desiredService.Namespace}, foundService)
	if err != nil && errors.IsNotFound(err) {
		// Service does not exist, so create it.
		log.Info("Creating a new Service", "Service.Namespace", desiredService.Namespace, "Service.Name", desiredService.Name)
		err = r.Create(ctx, desiredService)
		if err != nil {
			log.Error(err, "Failed to create new Service", "Service.Namespace", desiredService.Namespace, "Service.Name", desiredService.Name)
			return ctrl.Result{}, err
		}
	} else if err != nil {
		// Error getting the Service. Requeue.
		log.Error(err, "Failed to get Service")
		return ctrl.Result{}, err
	} else {
		// Service found. Check if an update is needed (simplified check for example).
		// In a real controller, you'd want a more robust comparison.
		if !serviceEqual(foundService.Spec, desiredService.Spec) {
			log.Info("Updating existing Service", "Service.Namespace", foundService.Namespace, "Service.Name", foundService.Name)
			foundService.Spec = desiredService.Spec
			err = r.Update(ctx, foundService)
			if err != nil {
				log.Error(err, "Failed to update Service", "Service.Namespace", foundService.Namespace, "Service.Name", foundService.Name)
				return ctrl.Result{}, err
			}
		} else {
			log.V(1).Info("Service is up-to-date", "Service.Namespace", foundService.Namespace, "Service.Name", foundService.Name)
		}
	}

	// 8. Update the App's status based on the actual state of its pods.
	// List pods managed by the Deployment created for this App.
	pods := &corev1.PodList{}
	listOpts := []client.ListOption{
		client.InNamespace(app.Namespace),
		client.MatchingLabels(map[string]string{"app": app.Name}), // Match pods by the common 'app' label
	}
	if err = r.List(ctx, pods, listOpts...); err != nil {
		log.Error(err, "Failed to list pods for App")
		return ctrl.Result{}, err
	}

	// Count ready pods.
	readyPods := int32(0)
	for _, pod := range pods.Items {
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				readyPods++
				break
			}
		}
	}

	// Update the App's status only if the number of ready replicas has changed.
	if app.Status.Replicas != readyPods {
		app.Status.Replicas = readyPods
		if err := r.Status().Update(ctx, app); err != nil {
			log.Error(err, "Failed to update App status")
			return ctrl.Result{}, err
		}
		log.Info("App status updated", "Replicas", app.Status.Replicas)
	}

	// 9. Requeue the request after a short duration. This ensures the controller
	// periodically re-checks the state, even if no events occur.
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

// deploymentEqual is a helper function to check if two DeploymentSpecs are functionally equivalent
// for our purposes (simplified for this example). In a production controller,
// this comparison would need to be much more robust, potentially using a deep equality library.
func deploymentEqual(a, b appsv1.DeploymentSpec) bool {
	// Pointers for Replicas need to be dereferenced for comparison
	if *a.Replicas != *b.Replicas {
		return false
	}
	if len(a.Template.Spec.Containers) != len(b.Template.Spec.Containers) {
		return false
	}
	if len(a.Template.Spec.Containers) > 0 {
		if a.Template.Spec.Containers[0].Image != b.Template.Spec.Containers[0].Image {
			return false
		}
		if len(a.Template.Spec.Containers[0].Ports) != len(b.Template.Spec.Containers[0].Ports) {
			return false
		}
		if len(a.Template.Spec.Containers[0].Ports) > 0 && a.Template.Spec.Containers[0].Ports[0].ContainerPort != b.Template.Spec.Containers[0].Ports[0].ContainerPort {
			return false
		}
	}
	return true
}

// serviceEqual is a helper function to check if two ServiceSpecs are functionally equivalent
// for our purposes (simplified for this example).
func serviceEqual(a, b corev1.ServiceSpec) bool {
	if a.Type != b.Type {
		return false
	}
	if len(a.Ports) != len(b.Ports) {
		return false
	}
	if len(a.Ports) > 0 && len(b.Ports) > 0 {
		if a.Ports[0].Port != b.Ports[0].Port || a.Ports[0].TargetPort.IntValue() != b.Ports[0].TargetPort.IntValue() || a.Ports[0].Protocol != b.Ports[0].Protocol {
			return false
		}
	}
	// You might want to compare selectors, cluster IP (if applicable), etc. for a more robust check.
	return true
}

// SetupWithManager sets up the controller with the Manager.
// It configures what resources the controller watches and which objects it owns.
func (r *AppReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&webappv1.App{}).       // The primary resource this controller watches
		Owns(&appsv1.Deployment{}). // Watches Deployments that are owned by an App
		Owns(&corev1.Service{}).    // Watches Services that are owned by an App
		Complete(r)
}
