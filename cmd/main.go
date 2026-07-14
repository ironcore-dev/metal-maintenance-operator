// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/tls"
	"flag"
	"os"
	"path/filepath"

	"github.com/ironcore-dev/metal-maintenance-operator/internal/cli"
	"github.com/ironcore-dev/metal-maintenance-operator/internal/discovery"
	"github.com/ironcore-dev/metal-maintenance-operator/internal/ignition"
	"github.com/ironcore-dev/metal-maintenance-operator/internal/server"
	telemetryruntime "github.com/ironcore-dev/metal-maintenance-operator/internal/telemetry/runtime"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/certwatcher"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	readinessv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/readiness/v1alpha1"
	vendorconsolev1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/vendorconsole/v1alpha1"
	maintenancectrl "github.com/ironcore-dev/metal-maintenance-operator/internal/controller/maintenance"
	readinessctrl "github.com/ironcore-dev/metal-maintenance-operator/internal/controller/readiness"
	vendorconsolectrl "github.com/ironcore-dev/metal-maintenance-operator/internal/controller/vendorconsole"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(vendorconsolev1alpha1.AddToScheme(scheme))
	utilruntime.Must(readinessv1alpha1.AddToScheme(scheme))
	utilruntime.Must(metalv1alpha1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

// nolint:gocyclo
func main() {
	var metricsAddr string
	var metricsCertPath, metricsCertName, metricsCertKey string
	var webhookCertPath, webhookCertName, webhookCertKey string
	var enableLeaderElection bool
	var leaderElectionNamespace string
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var tlsOpts []func(*tls.Config)
	var sanitizationNamespace string
	var sanitizationImage string
	var sanitizationTolerations []metalv1alpha1.Toleration
	var reportBaseURL string
	var sanitizedServerAddress string
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.StringVar(&leaderElectionNamespace, "leader-election-namespace", "",
		"Namespace used for leader election. Defaults to the pod namespace when running in-cluster.")
	flag.BoolVar(&secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	flag.StringVar(&webhookCertPath, "webhook-cert-path", "", "The directory that contains the webhook certificate.")
	flag.StringVar(&webhookCertName, "webhook-cert-name", "tls.crt", "The name of the webhook certificate file.")
	flag.StringVar(&webhookCertKey, "webhook-cert-key", "tls.key", "The name of the webhook key file.")
	flag.StringVar(&metricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	flag.StringVar(&metricsCertName, "metrics-cert-name", "tls.crt", "The name of the metrics server certificate file.")
	flag.StringVar(&metricsCertKey, "metrics-cert-key", "tls.key", "The name of the metrics server key file.")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	flag.StringVar(&sanitizationNamespace, "sanitization-namespace", "",
		"Namespace where sanitization ServerClaims are created.")
	flag.StringVar(&sanitizationImage, "sanitization-image", "", "OS image for the sanitization job.")
	cli.TolerationsVar(&sanitizationTolerations, "sanitization-tolerations", sanitizationTolerations,
		"Tolerations on the sanitization claim. Formatted key=[value]:effect.")
	flag.StringVar(&reportBaseURL, "report-base-url", "",
		"Base URL of the sanitization callback server "+
			"(e.g. http://metal-maintenance-operator.my-ns.svc.cluster.local:8082). "+
			"Sanitizers POST to <base-url>/<sanitizationUID> when done.")
	flag.StringVar(&sanitizedServerAddress, "sanitized-server-address", ":8082",
		"Address the sanitization callback HTTP server binds to. "+
			"Sanitizers running on bare metal POST here to report completion.")

	// Telemetry collector flags. The whole pipeline is gated by
	// --enable-telemetry — when off (the default), zero telemetry
	// Runnables are added to the manager and the operator behaves
	// exactly like a pure maintenance operator.
	var (
		enableTelemetry                bool
		telemetryConfigName            string
		telemetryConfigNamespace       string
		telemetryReceiverURL           string
		telemetryEventsAddr            string
		telemetryInsecureTLS           bool
		telemetryEnableCriticalHandler bool
		telemetrySubscriberID          string
	)
	flag.BoolVar(&enableTelemetry, "enable-telemetry", false,
		"Enable the BMC event-push pipeline: subscribes for Event-format pushes on event-eligible BMCs, "+
			"receives them at --telemetry-events-bind-address, and (optionally) writes a "+
			"CriticalEventReceived condition on matching Servers via the readiness bridge. "+
			"Metric scraping is a separate concern handled by an external redfish-exporter that "+
			"scrapes BMCs via the always-on /sd/bmcs service-discovery endpoint.")
	flag.StringVar(&telemetryConfigName, "telemetry-config-name", "telemetry-collector-config",
		"Name of the ConfigMap holding the telemetry configuration. Ignored when --enable-telemetry=false.")
	flag.StringVar(&telemetryConfigNamespace, "telemetry-config-namespace", "",
		"Namespace of the telemetry ConfigMap. Defaults to POD_NAMESPACE.")
	flag.StringVar(&telemetryReceiverURL, "telemetry-receiver-url", "",
		"Externally-reachable base URL BMCs POST events to. Required when --enable-telemetry is set.")
	flag.StringVar(&telemetryEventsAddr, "telemetry-events-bind-address", ":9092",
		"Listen address for the Redfish event receiver.")
	flag.BoolVar(&telemetryInsecureTLS, "telemetry-bmc-insecure-tls", false,
		"Skip TLS verification when the operator dials BMCs. Default false (secure). ")
	flag.BoolVar(&telemetryEnableCriticalHandler, "telemetry-enable-critical-event-handler", false,
		"When true, Critical-severity Redfish events set a CriticalEventReceived condition on the matching Server. "+
			"The operator does not create any ServerReadinessRule; apply "+
			"config/samples/serverreadinessrule-critical-event.yaml (or equivalent) by hand to consume the condition.")
	flag.StringVar(&telemetrySubscriberID, "telemetry-subscriber-id", "",
		"Single path segment that scopes this binary's subscriptions when multiple subscribers "+
			"(e.g. the maintenance operator and an external redfish-exporter) share a BMC fleet. "+
			"Becomes the <subscriberID> segment in /serverevents/<subscriberID>/alerts/<bmcName>. "+
			"Empty defaults to \"metal-maintenance-operator\" — set explicitly only when running "+
			"alongside another subscriber against the same BMCs.")

	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	if sanitizationNamespace == "" {
		setupLog.Error(nil, "Must specify --sanitization-namespace")
		os.Exit(1)
	}
	if sanitizationImage == "" {
		setupLog.Error(nil, "Must specify --sanitization-image")
		os.Exit(1)
	}
	if reportBaseURL == "" {
		setupLog.Error(nil, "Must specify --report-base-url")
		os.Exit(1)
	}

	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("disabling http/2")
		c.NextProtos = []string{"http/1.1"}
	}

	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	// Create watchers for metrics and webhooks certificates
	var metricsCertWatcher, webhookCertWatcher *certwatcher.CertWatcher

	// Initial webhook TLS options
	webhookTLSOpts := tlsOpts

	if len(webhookCertPath) > 0 {
		setupLog.Info("Initializing webhook certificate watcher using provided certificates",
			"webhook-cert-path", webhookCertPath, "webhook-cert-name", webhookCertName, "webhook-cert-key", webhookCertKey)

		var err error
		webhookCertWatcher, err = certwatcher.New(
			filepath.Join(webhookCertPath, webhookCertName),
			filepath.Join(webhookCertPath, webhookCertKey),
		)
		if err != nil {
			setupLog.Error(err, "Failed to initialize webhook certificate watcher")
			os.Exit(1)
		}

		webhookTLSOpts = append(webhookTLSOpts, func(config *tls.Config) {
			config.GetCertificate = webhookCertWatcher.GetCertificate
		})
	}

	webhookServer := webhook.NewServer(webhook.Options{
		TLSOpts: webhookTLSOpts,
	})

	// Metrics endpoint is enabled in 'config/default/kustomization.yaml'. The Metrics options configure the server.
	// More info:
	// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.21.0/pkg/metrics/server
	// - https://book.kubebuilder.io/reference/metrics.html
	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       tlsOpts,
	}

	if secureMetrics {
		// FilterProvider is used to protect the metrics endpoint with authn/authz.
		// These configurations ensure that only authorized users and service accounts
		// can access the metrics endpoint. The RBAC are configured in 'config/rbac/kustomization.yaml'. More info:
		// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.21.0/pkg/metrics/filters#WithAuthenticationAndAuthorization
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	// If the certificate is not specified, controller-runtime will automatically
	// generate self-signed certificates for the metrics server. While convenient for development and testing,
	// this setup is not recommended for production.
	//
	// TODO(user): If you enable certManager, uncomment the following lines:
	// - [METRICS-WITH-CERTS] at config/default/kustomization.yaml to generate and use certificates
	// managed by cert-manager for the metrics server.
	// - [PROMETHEUS-WITH-CERTS] at config/prometheus/kustomization.yaml for TLS certification.
	if len(metricsCertPath) > 0 {
		setupLog.Info("Initializing metrics certificate watcher using provided certificates",
			"metrics-cert-path", metricsCertPath, "metrics-cert-name", metricsCertName, "metrics-cert-key", metricsCertKey)

		var err error
		metricsCertWatcher, err = certwatcher.New(
			filepath.Join(metricsCertPath, metricsCertName),
			filepath.Join(metricsCertPath, metricsCertKey),
		)
		if err != nil {
			setupLog.Error(err, "to initialize metrics certificate watcher", "error", err)
			os.Exit(1)
		}

		metricsServerOptions.TLSOpts = append(metricsServerOptions.TLSOpts, func(config *tls.Config) {
			config.GetCertificate = metricsCertWatcher.GetCertificate
		})
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                  scheme,
		Metrics:                 metricsServerOptions,
		WebhookServer:           webhookServer,
		HealthProbeBindAddress:  probeAddr,
		LeaderElection:          enableLeaderElection,
		LeaderElectionID:        "88d880f0.metal.ironcore.dev",
		LeaderElectionNamespace: leaderElectionNamespace,
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err = (&vendorconsolectrl.ConsoleReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Unable to create Console controller")
		os.Exit(1)
	}

	if err = (&maintenancectrl.ServerSanitizationReconciler{
		Client:                  mgr.GetClient(),
		Scheme:                  mgr.GetScheme(),
		SanitizationNamespace:   sanitizationNamespace,
		SanitizationImage:       sanitizationImage,
		SanitizationTolerations: sanitizationTolerations,
		SanitizationIgnitionProvider: (&ignition.SanitizationProvider{
			ReportBaseURL: reportBaseURL,
		}).Ignition,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Unable to create ServerSanitization controller")
		os.Exit(1)
	}

	if err = (&server.SanitizedHandler{
		Client:                mgr.GetClient(),
		SanitizationNamespace: sanitizationNamespace,
		Address:               sanitizedServerAddress,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Unable to create Sanitized handler")
		os.Exit(1)
	}
	if err = (&readinessctrl.ServerWiringReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Unable to create ServerWiring controller")
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

	if enableTelemetry {
		ns := telemetryConfigNamespace
		if ns == "" {
			ns = os.Getenv("POD_NAMESPACE")
		}
		if ns == "" {
			setupLog.Error(nil,
				"Telemetry enabled but --telemetry-config-namespace and POD_NAMESPACE are both empty")
			os.Exit(1)
		}
		if err := telemetryruntime.AddTo(mgr, telemetryruntime.Options{
			ConfigName:                 telemetryConfigName,
			ConfigNamespace:            ns,
			ReceiverURL:                telemetryReceiverURL,
			EventsAddr:                 telemetryEventsAddr,
			InsecureTLS:                telemetryInsecureTLS,
			SubscriberID:               telemetrySubscriberID,
			EnableCriticalEventHandler: telemetryEnableCriticalHandler,
		}); err != nil {
			setupLog.Error(err, "Unable to add telemetry pipeline")
			os.Exit(1)
		}
		setupLog.Info("Telemetry pipeline enabled",
			"configMap", ns+"/"+telemetryConfigName,
			"receiverURL", telemetryReceiverURL)
	}

	// BMC service-discovery endpoint.
	sdHandler := &discovery.Handler{
		Client: mgr.GetClient(),
		Log:    ctrl.Log.WithName("discovery"),
	}
	if err := mgr.AddMetricsServerExtraHandler(discovery.Path, sdHandler); err != nil {
		setupLog.Error(err, "Unable to register SD handler", "path", discovery.Path)
		os.Exit(1)
	}
	if metricsAddr == "0" || metricsAddr == "" {
		setupLog.Info("BMC service discovery registered but unreachable: metrics server is disabled",
			"path", discovery.Path,
			"hint", "set --metrics-bind-address to enable")
	} else {
		setupLog.Info("BMC service discovery available on metrics server",
			"path", discovery.Path,
			"metricsBindAddress", metricsAddr)
	}

	if metricsCertWatcher != nil {
		setupLog.Info("Adding metrics certificate watcher to manager")
		if err := mgr.Add(metricsCertWatcher); err != nil {
			setupLog.Error(err, "unable to add metrics certificate watcher to manager")
			os.Exit(1)
		}
	}

	if webhookCertWatcher != nil {
		setupLog.Info("Adding webhook certificate watcher to manager")
		if err := mgr.Add(webhookCertWatcher); err != nil {
			setupLog.Error(err, "unable to add webhook certificate watcher to manager")
			os.Exit(1)
		}
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
