// quiet-push turns the relay's doorbell into a platform push.
//
// The relay's EN-3 rule POSTs a contentless ping to an https endpoint when
// mail lands and nobody is parked to hear it — or a park has gone silent
// (Doze). A phone without a UnifiedPush distributor has nowhere for that
// POST to land, and most phones have none. This is that somewhere: an
// endpoint per FCM registration token, and one job — forward the ping to
// Firebase Cloud Messaging as a HIGH-PRIORITY DATA MESSAGE WITH NO CONTENT,
// which is the one thing that wakes an Android phone out of Doze.
//
// WHAT CROSSES GOOGLE. A token Google issued, and the moment. No hint, no
// sender, no space, no count — the relay's ping carries none of that, and
// this forwards less: the FCM payload is a fixed marker. Google learns that
// this installation had something to check at this time; that is the whole
// price, and the app's own setting prints it.
//
// WHAT THIS KEEPS: nothing. No token is logged or stored past the request
// (a per-token coalescing map of timestamps, in memory, is the exception —
// it holds the token only long enough to refuse a second ping in the same
// half minute). The service-account key that signs requests to Google is
// the one secret here, read from disk at start.
//
//	quiet-push --listen 127.0.0.1:8993 --service-account /etc/quiet-push/sa.json
//	quiet-push --listen 127.0.0.1:8993 --dry-run     # accepts, forwards nothing
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8993", "address to serve on (behind nginx; never public)")
	saPath := flag.String("service-account", "", "Firebase service-account JSON (project_id, client_email, private_key)")
	dry := flag.Bool("dry-run", false, "accept pings and forward none — for standing the service up before a key exists")
	flag.Parse()

	var sender Sender
	switch {
	case *dry:
		sender = dryRun{}
		log.Printf("quiet-push: DRY RUN — pings are accepted and forwarded nowhere")
	case *saPath == "":
		fmt.Fprintln(os.Stderr, "quiet-push: --service-account is required (or --dry-run)")
		os.Exit(2)
	default:
		sa, err := LoadServiceAccount(*saPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "quiet-push: %v\n", err)
			os.Exit(2)
		}
		sender = NewFCM(sa)
		log.Printf("quiet-push: forwarding to FCM project %q", sa.ProjectID)
	}

	srv := &http.Server{
		Addr:              *listen,
		Handler:           NewHandler(sender),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()
	log.Printf("quiet-push: listening on %s", *listen)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
