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
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/spotify"

	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	ctx        context.Context
	conf       *oauth2.Config
	state      string
	baseURL    string
	port       string
	tls        bool
	certFile   string
	keyFile    string
	tokenFile  string
	secretName string
)

var clientset *kubernetes.Clientset

func setup() {
	flag.StringVar(&baseURL, "base-url", "https://localhost:8443", "")
	flag.StringVar(&port, "port", ":8443", "")
	flag.BoolVar(&tls, "tls", true, "")
	flag.StringVar(&certFile, "cert-file", "tls.crt", "")
	flag.StringVar(&keyFile, "key-file", "tls.key", "")
	flag.StringVar(&tokenFile, "token-file", "", "")
	flag.StringVar(&secretName, "secret-name", "", "")
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

type patchOperation struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value,omitempty"`
}

func refresh() {
	list, err := clientset.CoreV1().Namespaces().List(metav1.ListOptions{})
	if err != nil {
		log.Printf("Failed to list namespaces: %+v", err)
		return
	}

	for _, ns := range list.Items {
		refreshSingle(ns.Name, secretName)
	}
}

func refreshSingle(secretNamespace, secretName string) {
	secret, err := clientset.CoreV1().Secrets(secretNamespace).Get(secretName, metav1.GetOptions{})
	if err != nil {
		// This function scans all namespaces for secrets
		// If a secret is not found it's nothing to worry about :)
		if strings.HasSuffix(err.Error(), "not found") {
			return
		}

		log.Printf("Get secret failed: %s/%s: %+v", secretNamespace, secretName, err)
		return
	}

	log.Printf("Starting refresh of: %s/%s", secretNamespace, secretName)

	// reconstruct oauth2 object
	expiry, _ := time.Parse(time.RFC3339, string(secret.Data["expiry"]))
	token := &oauth2.Token{
		AccessToken:  string(secret.Data["accesstoken"]),
		RefreshToken: string(secret.Data["refreshtoken"]),
		Expiry:       expiry,
	}

	newToken, err := conf.TokenSource(oauth2.NoContext, token).Token()
	if err != nil {
		log.Fatalln(err)
	}

	if newToken.AccessToken != token.AccessToken {
		token = newToken
		log.Println("Access token has changed")
	}

	if tokenFile != "" {
		err = ioutil.WriteFile(tokenFile, []byte(token.AccessToken), 0644)
		if err == nil {
			log.Println("Updated token in", tokenFile)
		} else {
			log.Fatal(err)
		}
	}

	if secretName != "" && secretNamespace != "" && clientset != nil {
		secrets := clientset.CoreV1().Secrets(secretNamespace)
		_, err := secrets.Get(secretName, metav1.GetOptions{})
		if err == nil {
			patch := []patchOperation{
				patchOperation{
					Op:    "add",
					Path:  "/stringData",
					Value: make(map[string]string),
				},
				patchOperation{
					Op:    "add",
					Path:  "/stringData/accesstoken",
					Value: token.AccessToken,
				},
				patchOperation{
					Op:    "add",
					Path:  "/stringData/refreshtoken",
					Value: token.RefreshToken,
				},
				patchOperation{
					Op:    "add",
					Path:  "/stringData/expiry",
					Value: token.Expiry.Format(time.RFC3339),
				},
				patchOperation{
					Op:    "add",
					Path:  "/stringData/updated",
					Value: time.Now().Format(time.RFC3339),
				},
			}
			raw, err := json.Marshal(patch)
			if err != nil {
				fmt.Println(err)
			}
			fin, err := secrets.Patch(secretName, types.JSONPatchType, raw)
			if err == nil {
				log.Printf("Patched secret %s/%s with new token", secretNamespace, secretName)
			} else {
				fmt.Println(err)
				fmt.Println(fin)
			}
		} else {
			fmt.Println(err.Error())
			createTokenSecret(token, secretNamespace, secretName)
		}
	}
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
	log.Printf("Create namespace: %s: %+v", secretNamespace, err)

	// Create new
	s := apiv1.Secret{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Secret",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: secretNamespace,
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
		fmt.Println("ERROR:", err)
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

	state = uuid.New().String()
	log.Println("State:", state)
	http.HandleFunc("/auth", func(w http.ResponseWriter, r *http.Request) {
		url := conf.AuthCodeURL(state, oauth2.SetAuthURLParam("show_dialog", "true"))
		http.Redirect(w, r, url, 302)
	})

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "ok")
	})

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		authURL := baseURL + "/auth"
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, "Hello world, go to: <a href=\"%s\">%s</a>", authURL, authURL)
	})

	http.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		u := r.URL
		if state != u.Query().Get("state") {
			log.Fatal("Invalid state")
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
		fmt.Fprintf(w, fmt.Sprintf("Welcome, %s!", username))
	})

	s := http.Server{
		Addr: port,
		Handler: func(handler http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/healthz" {
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

	// perform initial refresh
	refresh()

	ticker := time.NewTicker(10 * 60 * time.Second)
	quit := make(chan bool)
	go func() {
		for {
			select {
			case <-ticker.C:
				refresh()
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
