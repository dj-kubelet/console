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
	token           *oauth2.Token
	ctx             context.Context
	conf            *oauth2.Config
	state           string
	baseURL         string
	port            string
	tls             bool
	certFile        string
	keyFile         string
	tokenFile       string
	secretNamespace string
	secretName      string
)
var clientset *kubernetes.Clientset

func setup() {
	flag.StringVar(&baseURL, "base-url", "https://localhost:8443", "")
	flag.StringVar(&port, "port", ":8443", "")
	flag.BoolVar(&tls, "tls", true, "")
	flag.StringVar(&certFile, "cert-file", "tls.crt", "")
	flag.StringVar(&keyFile, "key-file", "tls.key", "")
	flag.StringVar(&tokenFile, "token-file", "", "")
	flag.StringVar(&secretNamespace, "secret-namespace", "", "")
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
					"accesstoken": []byte(token.AccessToken),
				},
				Type: "Opaque",
			}
			_, err := secrets.Create(&s)
			if err == nil {
				log.Printf("Created secret %s/%s", secretNamespace, secretName)
			} else {
				fmt.Println("ERROR:", err)
			}
		}

	}

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
		if token == nil {
			fmt.Fprintf(w, "Hello world, go to: %s", baseURL+"/auth")
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
		defer refresh()

		http.Redirect(w, r, baseURL, 302)
	})

	s := http.Server{
		Addr: port,
		Handler: func(handler http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				log.Printf("%s %s %s\n", r.RemoteAddr, r.Method, r.URL)
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
