package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"runtime"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	cloudaws "github.com/openshift/karpenter-operator/pkg/cloudprovider/aws"
	"github.com/openshift/karpenter-operator/pkg/operator"
	"github.com/openshift/karpenter-operator/pkg/version"
)

var (
	setupLog = ctrl.Log.WithName("setup")
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "check-credentials" {
		os.Exit(runCheckCredentials())
	}

	var opts operator.Options

	flag.StringVar(&opts.Namespace, "namespace", "", "The namespace to deploy karpenter into")
	flag.StringVar(&opts.MetricsAddr, "metrics-bind-address", ":8080", "The address the metrics endpoint binds to")
	flag.StringVar(&opts.ProbeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to")
	flag.BoolVar(&opts.LeaderElect, "leader-elect", false, "Enable leader election for controller manager")

	zapOpts := zap.Options{Development: false}
	zapOpts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))

	setupLog.Info("starting", "version", version.String, "go", runtime.Version(), "os", runtime.GOOS, "arch", runtime.GOARCH)

	opts.LoadEnv()

	if err := opts.Validate(); err != nil {
		setupLog.Error(err, "invalid configuration")
		os.Exit(1)
	}

	if err := operator.Run(ctrl.SetupSignalHandler(), opts); err != nil {
		setupLog.Error(err, "unable to run operator")
		os.Exit(1)
	}
}

// Canonical list of environment variables injected by the operator which map to the cloud provider OCP is running on.
// The operator must be configured with one of the below environment variables, based on the infrastructure.
const (
	envAWS = "AWS_REGION"
	// envAzure = "TODO"
	// envGCP = "TODO"
)

// runCheckCredentials is an init container entrypoint that blocks until the
// cloud provider API is reachable with the provisioned credentials. Each
// provider implements its own readiness check (e.g. an EC2 call for AWS).
// The provider is detected from a canonical list of environment variables injected by the operator.
func runCheckCredentials() int {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var err error
	switch {
	case os.Getenv(envAWS) != "":
		err = cloudaws.CheckCredentials(ctx, os.Getenv(envAWS))
	default:
		fmt.Fprintln(os.Stderr, "no recognized cloud provider environment variables found")
		return 1
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "check-credentials failed: %v\n", err)
		return 1
	}
	fmt.Println("credentials validated successfully")
	return 0
}
