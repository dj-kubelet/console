package main

import (
	"context"
	"encoding/json"
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
	"github.com/gorilla/sessions"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/spotify"

	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	ctx        context.Context
	conf       *oauth2.Config
	baseURL    string
	port       string
	tls        bool
	certFile   string
	keyFile    string
	secretName string
	key        = []byte("super-secret-key")
	store      = sessions.NewCookieStore(key)
)

var clientset *kubernetes.Clientset

func setup() {
	flag.StringVar(&baseURL, "base-url", "https://localhost:8443", "")
	flag.StringVar(&port, "port", ":8443", "")
	flag.BoolVar(&tls, "tls", true, "")
	flag.StringVar(&certFile, "cert-file", "tls.crt", "")
	flag.StringVar(&keyFile, "key-file", "tls.key", "")
	flag.StringVar(&secretName, "secret-name", "spotify-oauth", "")
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

	config, err := rest.InClusterConfig()
	if err != nil {
		log.Println("Failed to set up in cluster configuration, testing with kubeconfig")
		kubeconfig := os.Getenv("KUBECONFIG")
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			log.Fatalln(err.Error())
		}
	}

	clientset = kubernetes.NewForConfigOrDie(config)
}

func createTokenSecret(token *oauth2.Token, spotifyUsername, secretName string) {
	secretNamespace := fmt.Sprintf("spotify-%s", spotifyUsername)

	// Create the namespace if it doesn't exist
	_, err := clientset.CoreV1().Namespaces().Create(&apiv1.Namespace{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Namespace",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: secretNamespace,
		},
	})
	if err == nil {
		log.Printf("Created namespace: %s\n", secretNamespace)
	} else {
		log.Printf("%+v\n", err)
	}

	// Create new
	s := apiv1.Secret{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Secret",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: secretNamespace,
			Labels: map[string]string{
				"dj-kubelet.com/oauth-refresher": "spotify",
			},
		},
		Data: map[string][]byte{
			"accesstoken":  []byte(token.AccessToken),
			"refreshtoken": []byte(token.RefreshToken),
			"expiry":       []byte(token.Expiry.Format(time.RFC3339)),
			"updated":      []byte(time.Now().Format(time.RFC3339)),
		},
		Type: "Opaque",
	}

	secrets := clientset.CoreV1().Secrets(secretNamespace)
	_, err = secrets.Create(&s)
	if err == nil {
		log.Printf("Created secret %s/%s", secretNamespace, secretName)
	} else {
		log.Printf("%+v\n", err)
	}
}

func getSpotifyUsername(token *oauth2.Token) (string, error) {
	client := conf.Client(ctx, token)
	res, err := client.Get("https://api.spotify.com/v1/me")
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return "", err
	}

	type spotifyMe struct {
		ID string `json:"id"` // id or username
	}

	var me spotifyMe
	err = json.Unmarshal(body, &me)
	if err != nil {
		return "", err
	}
	return me.ID, nil
}

func main() {
	setup()

	http.HandleFunc("/auth", func(w http.ResponseWriter, r *http.Request) {
		session, _ := store.Get(r, "user")
		state := uuid.New().String()
		session.Values["oauth-state"] = state
		session.Save(r, w)

		url := conf.AuthCodeURL(state, oauth2.SetAuthURLParam("show_dialog", "true"))
		http.Redirect(w, r, url, 302)
	})

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "ok")
	})

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		session, _ := store.Get(r, "user")
		username := session.Values["username"]
		if username != nil {
			fmt.Fprintf(w, "Welcome, %s!", username)
			// TODO Verify namespace created
		} else {
			authURL := baseURL + "/auth"
			fmt.Fprintf(w, "Hello world, go to: <a href=\"%s\">%s</a>", authURL, authURL)
		}
	})

	http.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		u := r.URL
		session, _ := store.Get(r, "user")
		if session.Values["oauth-state"] != u.Query().Get("state") {
			log.Println("Invalid state")
			return
		}
		token, err := conf.Exchange(ctx, u.Query().Get("code"))
		if err != nil {
			log.Fatal(err)
		}

		username, err := getSpotifyUsername(token)
		if err != nil {
			log.Printf("failed to load username: %+v", err)
		}

		go createTokenSecret(token, username, secretName)

		session.Values["username"] = username
		session.Save(r, w)

		http.Redirect(w, r, baseURL, 302)
	})

	s := http.Server{
		Addr: port,
		Handler: func(handler http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/health" {
					log.Printf("%s %s %s\n", r.RemoteAddr, r.Method, r.URL.Path)
				}
				handler.ServeHTTP(w, r)
			})
		}(http.DefaultServeMux),
	}
	go func() {
		if tls {
			log.Fatal(s.ListenAndServeTLS(certFile, keyFile))
		} else {
			log.Fatal(s.ListenAndServe())
		}
	}()

	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)
	<-signalChan

	log.Println("Shutdown signal received, exiting...")

	s.Shutdown(context.Background())
}
