package main

import (
	"context"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/spotify"
)

var (
	token     *oauth2.Token
	ctx       context.Context
	conf      *oauth2.Config
	state     string
	baseURL   string
	port      string
	tls       bool
	certFile  string
	keyFile   string
	tokenFile string
)

func setup() {
	flag.StringVar(&baseURL, "base-url", "https://localhost:8443", "")
	flag.StringVar(&port, "port", ":8443", "")
	flag.BoolVar(&tls, "tls", true, "")
	flag.StringVar(&certFile, "cert-file", "server.pem", "")
	flag.StringVar(&keyFile, "key-file", "server-key.pem", "")
	flag.StringVar(&tokenFile, "token-file", "", "")
	flag.Parse()

	clientID, ok := os.LookupEnv("CLIENT_ID")
	if !ok {
		log.Fatalln("env CLIENT_ID not set")
	}
	clientSecret, ok := os.LookupEnv("CLIENT_SECRET")
	if !ok {
		log.Fatalln("env CLIENT_SECRET not set")
	}

	ctx = context.Background()
	conf = &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Scopes: []string{
			// Provide info about which user that's signed in.
			"user-read-private",
			// Not used but could be useful?
			"user-read-currently-playing",
			// To read the current playing song
			"user-read-playback-state",
			// For switching song
			"user-modify-playback-state",
		},
		RedirectURL: baseURL + "/callback",
		Endpoint:    spotify.Endpoint,
	}
}

func refresh() {
	// Force refresh token
	token.Expiry = time.Now()

	newToken, err := conf.TokenSource(oauth2.NoContext, token).Token()
	if err != nil {
		log.Fatalln(err)
	}

	if newToken.AccessToken != token.AccessToken {
		token = newToken
		log.Println("Saved new token")
	}

	if tokenFile != "" {
		message := []byte(token.AccessToken)
		err = ioutil.WriteFile(tokenFile, message, 0644)
		if err != nil {
			log.Fatal(err)
		}
		log.Println("Updated token in", tokenFile)
	}
}

func main() {
	setup()

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if token == nil {
			state = uuid.New().String()
			url := conf.AuthCodeURL(state, oauth2.SetAuthURLParam("show_dialog", "true"))
			http.Redirect(w, r, url, 302)
		} else {
			client := conf.Client(ctx, token)
			res, err := client.Get("https://api.spotify.com/v1/me")
			if err != nil {
				log.Fatal(err)
			}
			body, err := ioutil.ReadAll(res.Body)
			res.Body.Close()
			if err != nil {
				log.Fatal(err)
			}
			fmt.Fprintf(w, string(body))
		}
	})

	http.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		u := r.URL
		if state != u.Query().Get("state") {
			log.Fatal("Invalid state")
		}
		t, err := conf.Exchange(ctx, u.Query().Get("code"))
		if err != nil {
			log.Fatal(err)
		}
		token = t
		refresh()

		http.Redirect(w, r, baseURL, 302)
	})

	s := http.Server{
		Addr:    port,
		Handler: http.DefaultServeMux}
	go func() {
		if tls {
			log.Fatal(s.ListenAndServeTLS(certFile, keyFile))
		} else {
			log.Fatal(s.ListenAndServe())
		}
	}()

	ticker := time.NewTicker(10 * 60 * time.Second)
	quit := make(chan bool)
	go func() {
		for {
			select {
			case <-ticker.C:
				if token != nil {
					refresh()
				}
			case <-quit:
				ticker.Stop()
				return
			}
		}
	}()

	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)
	<-signalChan

	log.Println("Shutdown signal received, exiting...")

	quit <- true
	s.Shutdown(context.Background())
}
