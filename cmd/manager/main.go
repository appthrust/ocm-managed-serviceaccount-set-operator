package main

import (
	"errors"
	"flag"
	"net/http"
	"os"

	authv1alpha1 "github.com/appthrust/ocm-managed-serviceaccount-replicaset-controller/api/v1alpha1"
	"github.com/appthrust/ocm-managed-serviceaccount-replicaset-controller/internal/controller"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clusterv1beta1 "open-cluster-management.io/api/cluster/v1beta1"
	workv1 "open-cluster-management.io/api/work/v1"
	msav1beta1 "open-cluster-management.io/managed-serviceaccount/apis/authentication/v1beta1"
	civ1alpha1 "sigs.k8s.io/cluster-inventory-api/apis/v1alpha1"
	"sigs.k8s.io/cluster-inventory-api/pkg/access"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(authv1alpha1.AddToScheme(scheme))
	utilruntime.Must(clusterv1beta1.Install(scheme))
	utilruntime.Must(msav1beta1.AddToScheme(scheme))
	utilruntime.Must(workv1.Install(scheme))
	utilruntime.Must(civ1alpha1.AddToScheme(scheme))
}

func main() {
	var metricsAddr string
	var healthAddr string
	var leaderElect bool
	var clusterProfileProviderFile string
	var zapOptions zap.Options

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the secured metrics endpoint binds to. Authenticated via Kubernetes TokenReview and authorized via SubjectAccessReview.")
	flag.StringVar(&healthAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&leaderElect, "leader-elect", false, "Enable leader election for controller manager.")
	flag.StringVar(&clusterProfileProviderFile, "clusterprofile-provider-file", "", "Path to the ClusterProfile access-provider configuration file for selector-targeted namespace grants.")
	zapOptions.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOptions)))
	log := ctrl.Log.WithName("setup")

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress:    metricsAddr,
			SecureServing:  true,
			FilterProvider: filters.WithAuthenticationAndAuthorization,
		},
		HealthProbeBindAddress: healthAddr,
		LeaderElection:         leaderElect,
		LeaderElectionID:       "ocm-managed-serviceaccount-replicaset-controller.authentication.appthrust.io",
	})
	if err != nil {
		log.Error(err, "unable to start manager")
		os.Exit(1)
	}

	var namespaceResolver controller.NamespaceSelectorResolver
	if clusterProfileProviderFile != "" {
		credentialsProvider, err := access.NewFromFile(clusterProfileProviderFile)
		if err != nil {
			log.Error(err, "unable to load ClusterProfile access-provider configuration")
			os.Exit(1)
		}
		resolver := &controller.ClusterInventoryNamespaceResolver{
			LocalClient:  mgr.GetClient(),
			AccessConfig: credentialsProvider,
		}
		if err := mgr.Add(resolver); err != nil {
			log.Error(err, "unable to register namespace resolver runnable")
			os.Exit(1)
		}
		namespaceResolver = resolver
	}

	if err := (&controller.ManagedServiceAccountReplicaSetReconciler{
		NamespaceSelectorResolver: namespaceResolver,
	}).SetupWithManager(mgr); err != nil {
		log.Error(err, "unable to create controller", "controller", "ManagedServiceAccountReplicaSet")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		log.Error(err, "unable to set up health check")
		os.Exit(1)
	}

	signalCtx := ctrl.SetupSignalHandler()

	cacheReady := make(chan struct{})
	go func() {
		if mgr.GetCache().WaitForCacheSync(signalCtx) {
			close(cacheReady)
		}
	}()

	if err := mgr.AddReadyzCheck("readyz", func(_ *http.Request) error {
		select {
		case <-cacheReady:
			return nil
		default:
			return errors.New("informer cache has not yet synced")
		}
	}); err != nil {
		log.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	log.Info("starting manager")
	if err := mgr.Start(signalCtx); err != nil {
		log.Error(err, "manager exited")
		os.Exit(1)
	}
}
