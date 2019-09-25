package main

import (
	"context"
	"encoding/base64"
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
	rbacv1 "k8s.io/api/rbac/v1"
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

func getKubeconfig(spotifyUsername string, apiserver string) string {
	secretNamespace := fmt.Sprintf("spotify-%s", spotifyUsername)
	sa, err := clientset.CoreV1().ServiceAccounts(secretNamespace).Get(spotifyUsername, metav1.GetOptions{})
	if err == nil {
		log.Printf("Got serviceaccount: %s\n", sa.ObjectMeta.Name)
		log.Printf("sa secret: %s\n", sa.Secrets[0].Name)
	} else {
		log.Printf("%+v\n", err)
	}
	secret, err := clientset.CoreV1().Secrets(secretNamespace).Get(sa.Secrets[0].Name, metav1.GetOptions{})
	if err == nil {
		kc := `apiVersion: v1
clusters:
- cluster:
    server: %s
    certificate-authority-data: %s
  name: dj-kubelet
contexts:
- context:
    cluster: dj-kubelet
    namespace: %s
    user: user
  name: user@dj-kubelet
current-context: user@dj-kubelet
kind: Config
preferences: {}
users:
- name: user
  user:
    token: %s
`
		return fmt.Sprintf(kc, apiserver, base64.StdEncoding.EncodeToString(secret.Data["ca.crt"]), secretNamespace, secret.Data["token"])
	} else {
		log.Printf("%+v\n", err)
	}
	return "NO Kubeconfig"
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

	// Create ServiceAccount
	_, err = clientset.CoreV1().ServiceAccounts(secretNamespace).Create(&apiv1.ServiceAccount{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ServiceAccount",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: spotifyUsername,
		},
	})
	if err == nil {
		log.Printf("Created serviceaccount: %s\n", secretNamespace)
	} else {
		log.Printf("%+v\n", err)
	}

	// Create ClusterRoleBinding
	_, err = clientset.RbacV1().ClusterRoleBindings().Create(&rbacv1.ClusterRoleBinding{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ClusterRoleBinding",
			APIVersion: "rbac.authorization.k8s.io/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: "dj-kubelet:" + spotifyUsername,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "dj-kubelet:user-global",
		},
		Subjects: []rbacv1.Subject{rbacv1.Subject{
			Kind:      "ServiceAccount",
			Name:      "matti4s",
			Namespace: secretNamespace,
		}},
	})
	if err == nil {
		log.Printf("Created clusterrolebinding: %s\n", "dj-kubelet:"+spotifyUsername)
	} else {
		log.Printf("%+v\n", err)
	}

	// Create RoleBinding
	_, err = clientset.RbacV1().RoleBindings(secretNamespace).Create(&rbacv1.RoleBinding{
		TypeMeta: metav1.TypeMeta{
			Kind:       "RoleBinding",
			APIVersion: "rbac.authorization.k8s.io/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: "dj-kubelet:" + spotifyUsername,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "dj-kubelet:user",
		},
		Subjects: []rbacv1.Subject{rbacv1.Subject{
			Kind:      "ServiceAccount",
			Name:      "matti4s",
			Namespace: secretNamespace,
		}},
	})
	if err == nil {
		log.Printf("Created rolebinding: %s\n", "dj-kubelet:"+spotifyUsername)
	} else {
		log.Printf("%+v\n", err)
	}

	// Create new oauth secret
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

func loginSpotify(w http.ResponseWriter, r *http.Request) {
	session, _ := store.Get(r, "user")
	state := uuid.New().String()
	session.Values["spotify-oauth-state"] = state
	session.Save(r, w)

	url := conf.AuthCodeURL(state, oauth2.SetAuthURLParam("show_dialog", "true"))
	http.Redirect(w, r, url, 302)
}

func health(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "ok")
}

func index(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	session, _ := store.Get(r, "user")
	username := session.Values["username"]
	if username != nil {
		// TODO Verify namespace created
		kubeconfig := getKubeconfig(username.(string), "https://localhost:44091")
		fmt.Fprintf(w, `<p>Nice to have you here %s! Let's rock and roll!</p><textarea cols="80" rows="20" style="white-space: pre; width">%s</textarea>`, username, kubeconfig)
	} else {
		authURL := baseURL + "/login/spotify"
		fmt.Fprintf(w, "<p>Hello there. This is dj-kubelet.</p><p><a href=\"%s\">Log in with Spotify</a></p>", authURL)
	}
}

func callback(w http.ResponseWriter, r *http.Request) {
	u := r.URL
	session, _ := store.Get(r, "user")
	if session.Values["spotify-oauth-state"] != u.Query().Get("state") {
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
}

func main() {
	setup()

	http.HandleFunc("/", index)
	http.HandleFunc("/health", health)
	http.HandleFunc("/login/spotify", loginSpotify)
	http.HandleFunc("/callback", callback)

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
