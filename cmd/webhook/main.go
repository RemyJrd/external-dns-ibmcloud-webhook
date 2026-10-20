package main

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/networking-go-sdk/dnssvcsv1"
	log "github.com/sirupsen/logrus"

	"sigs.k8s.io/external-dns/endpoint"
	"sigs.k8s.io/external-dns/provider/webhook/api"

	"gitlab.com/estreem-eu/sre/devfactory/ocp-gitops/external-dns-ibmcloud-webhook/internal/ibmdns"
)

func main() {
	log.SetFormatter(&log.JSONFormatter{})
	level, err := log.ParseLevel(env("LOG_LEVEL", "info"))
	if err != nil {
		log.Fatalf("LOG_LEVEL: %v", err)
	}
	log.SetLevel(level)

	apiKey := required("IBMCLOUD_API_KEY")
	instanceID := required("DNS_INSTANCE_ID")
	domains := strings.Split(required("DOMAIN_FILTER"), ",")
	dryRun, err := strconv.ParseBool(env("DRY_RUN", "false"))
	if err != nil {
		log.Fatalf("DRY_RUN: %v", err)
	}

	client, err := dnssvcsv1.NewDnsSvcsV1(&dnssvcsv1.DnsSvcsV1Options{
		URL: env("DNS_API_URL", "https://api.private.dns-svcs.cloud.ibm.com/v1"),
		Authenticator: &core.IamAuthenticator{
			ApiKey: apiKey,
			URL:    env("IAM_URL", "https://private.iam.cloud.ibm.com"),
		},
	})
	if err != nil {
		log.Fatalf("DNS Services client: %v", err)
	}

	p := ibmdns.New(client, instanceID, endpoint.NewDomainFilter(domains), dryRun)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	zones, err := p.Zones(ctx)
	cancel()
	if err != nil {
		log.Fatalf("startup check: %v", err)
	}
	if len(zones) == 0 {
		log.Fatalf("startup check: no zone of instance %s matches %v", instanceID, domains)
	}
	log.WithField("zones", zones).WithField("dryRun", dryRun).Info("Managing zones")

	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
		srv := &http.Server{Addr: env("HEALTH_ADDR", ":8080"), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		log.Fatal(srv.ListenAndServe())
	}()

	api.StartHTTPApi(api.ServerOptions{
		Provider:          p,
		ProviderPort:      env("PROVIDER_ADDR", "127.0.0.1:8888"),
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      2 * time.Minute,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       time.Minute,
		MaxBodySize:       10 << 20,
	})
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func required(name string) string {
	v := os.Getenv(name)
	if v == "" {
		log.Fatalf("%s is required", name)
	}
	return v
}
