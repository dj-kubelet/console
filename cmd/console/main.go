package main

import (
	"encoding/base64"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"golang.org/x/oauth2"

	"github.com/gorilla/sessions"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	apiv1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/dj-kubelet/console/pkg/auth"
)

var (
	conf       *oauth2.Config
	baseURL    string
	port       string
	certFile   string
	keyFile    string
	secretName string
	key        = []byte("super-secret-key")
	sauther    auth.Auther
)

var clientset *kubernetes.Clientset

func setup() {
	flag.StringVar(&baseURL, "base-url", "https://localhost:8443", "")
	flag.StringVar(&port, "port", ":8443", "")
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

	sauther = auth.New(clientID, clientSecret, baseURL+"/callback")

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
		if len(sa.Secrets) == 0 {
			log.Printf("No secrets found!")
		} else {
			log.Printf("sa secret: %s\n", sa.Secrets[0].Name)
		}
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

func createNamespace(token *oauth2.Token, spotifyUsername string) string {
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
	return secretNamespace
}

func createTokenSecret(token *oauth2.Token, secretNamespace, secretName string) {
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
	_, err := secrets.Create(&s)
	if err == nil {
		log.Printf("Created secret %s/%s", secretNamespace, secretName)
	} else {
		log.Printf("%+v\n", err)
	}
}

func health(c echo.Context) error {
	return c.String(http.StatusOK, "ok")
}

func index(c echo.Context) error {
	sess, _ := session.Get("session", c)
	username := sess.Values["username"]
	if username != nil {
		// TODO Verify namespace created
		kubeconfig := getKubeconfig(username.(string), "https://localhost:44091")
		content := template.HTML(fmt.Sprintf(`
<p>Nice to have you here %s!</p>
<p>Let's rock and roll!</p>
<textarea id="kubeconfig" cols=80 rows=20 spellcheck="false" class="code">%s</textarea>
<button type="button" onclick="selectAndCopyKubeconfig()">Copy Kubeconfig</button>
`, username, kubeconfig))
		return c.Render(http.StatusOK, "index.html", content)
	} else {
		loginURL := "/login/spotify"
		content := template.HTML(fmt.Sprintf(`
<p>Hello there. This is dj-kubelet.</p>
<p><a href="%s">Log in with Spotify</a></p>
`, loginURL))
		return c.Render(http.StatusOK, "index.html", content)
	}
}

func callback(c echo.Context) error {
	username, token, err := sauther.Callback(c)
	if err != nil {
		log.Printf("failed to load username: %+v", err)
	}

	go func() {
		ns := createNamespace(token, username)
		createTokenSecret(token, ns, secretName)
	}()

	return c.Redirect(http.StatusTemporaryRedirect, baseURL)
}

type Template struct {
	templates *template.Template
}

func (t *Template) Render(w io.Writer, name string, data interface{}, c echo.Context) error {
	return t.templates.ExecuteTemplate(w, name, data)
}

func main() {
	setup()

	e := echo.New()
	t := &Template{
		templates: template.Must(template.ParseGlob("templates/*.html")),
	}
	e.Renderer = t

	e.Use(session.Middleware(sessions.NewCookieStore(key)))
	e.Use(middleware.Logger())
	//e.Use(middleware.Recover())

	e.Static("/static", "static")

	e.GET("/", index)
	e.GET("/health", health)
	e.GET("/login/spotify", sauther.Auth)
	e.GET("/callback", callback)

	e.Logger.Fatal(e.StartTLS(port, certFile, keyFile))
}
