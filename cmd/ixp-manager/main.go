package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/moedove/ixp-manager/internal/app"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8080", "HTTP listen address")
	data := flag.String("data", "./var", "private persistent data directory")
	web := flag.String("web", defaultWebDirectory(), "built frontend directory")
	bird := flag.String("bird", "bird", "BIRD 2 executable for read-only validation")
	bird1 := flag.String("bird1", "bird1", "actual BIRD 1 IPv4 executable; no BIRD 2/3 or family fallback")
	bird1IPv6 := flag.String("bird1-ipv6", "bird6", "actual BIRD 1 IPv6 executable; independent of --bird1")
	bird3 := flag.String("bird3", "bird3", "actual BIRD 3 executable; never falls back to BIRD 2")
	openbgpd := flag.String("openbgpd", "openbgpd", "actual OpenBGPD executable (for example /usr/sbin/bgpd)")
	frr := flag.String("frr", "bgpd", "FRRouting bgpd executable for native dry-run")
	quagga := flag.String("quagga-bgpd", "quagga-bgpd", "actual Quagga 1.2.4 bgpd; mandatory private user/network namespace, no FRR fallback")
	namedCheckzone := flag.String("named-checkzone", "named-checkzone", "BIND named-checkzone executable for reverse-zone validation")
	pdnsServer := flag.String("pdns-server", "pdns_server", "PowerDNS Authoritative executable for AS112 bundle configuration checks")
	documentQuota := flag.Int64("document-quota", 1<<30, "maximum stored document and revision bytes")
	trustedProxies := flag.String("trusted-proxies", "", "comma-separated trusted reverse-proxy IPs/CIDRs; empty ignores forwarding headers")
	providerConfig := flag.String("provider-config", "", "private local allowlisted external-provider configuration")
	atlasConfig := flag.String("atlas-config", "", "private RIPE Atlas configuration; omitted disables external measurements")
	smtpConfig := flag.String("smtp-config", "", "private local SMTP JSON configuration; omitted disables delivery")
	overallTraffic := flag.String("legacy-overall-traffic", "", "absolute path to a reviewed legacy ixp001 yearly overall traffic snapshot")
	overallTrafficSHA := flag.String("legacy-overall-traffic-sha256", "", "reviewed SHA-256 of the legacy traffic snapshot")
	publishOverallTraffic := flag.Bool("publish-legacy-overall-traffic", false, "allow anonymous use of the legacy monthly overall traffic API")
	secure := flag.Bool("secure-cookie", false, "require HTTPS cookies (enable behind TLS proxy)")
	flag.Parse()
	if *documentQuota < 1 {
		log.Fatal("document-quota must be positive")
	}
	a, err := app.New(app.Config{
		DataDir: *data, WebDir: *web, SecureCookie: *secure, DocumentQuota: *documentQuota,
		SetupToken: os.Getenv("IXP_SETUP_TOKEN"), TrustedProxies: strings.Split(*trustedProxies, ","),
		BirdBinary: *bird, Bird1Binary: *bird1, Bird1IPv6Binary: *bird1IPv6, Bird3Binary: *bird3,
		OpenBGPDBinary: *openbgpd, FRRBinary: *frr, QuaggaBinary: *quagga,
		NamedCheckzone: *namedCheckzone, PDNSServer: *pdnsServer,
		ProviderConfig: *providerConfig, AtlasConfig: *atlasConfig, SMTPConfig: *smtpConfig,
		OverallTrafficFile: *overallTraffic, OverallTrafficSHA256: *overallTrafficSHA,
		PublishOverallTraffic: *publishOverallTraffic,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer a.Close()
	if token := a.BootstrapToken(); token != "" && os.Getenv("IXP_SETUP_TOKEN") == "" {
		log.Printf("首次初始化令牌（仅首次设置使用）: %s", token)
	}
	srv := &http.Server{Addr: *listen, Handler: a, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a.StartBackground(ctx)
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Printf("MoeDove IXP listening on %s", *listen)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
