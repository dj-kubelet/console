package main

import (
	"context"
	"encoding/base64"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"golang.org/x/oauth2"

	"github.com/gorilla/securecookie"
	"github.com/gorilla/sessions"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	appsv1 "k8s.io/api/apps/v1"
	apiv1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/dj-kubelet/console/pkg/auth"
)

var (
	baseURL           string
	port              string
	certFile          string
	keyFile           string
	secretName        string
	apiserverEndpoint string
	sauther           auth.Auther
	// Validate to be 32 or 64 bytes
	cookieStoreAuthKey string
	// Validate to be 32 bytes
	cookieStoreEncryptionKey string
)

var clientset *kubernetes.Clientset

func setup() {
	flag.StringVar(&baseURL, "base-url", "https://localhost:8443", "")
	flag.StringVar(&port, "port", ":8443", "")
	flag.StringVar(&certFile, "cert-file", "tls.crt", "")
	flag.StringVar(&keyFile, "key-file", "tls.key", "")
	flag.StringVar(&secretName, "secret-name", "spotify-oauth", "")
	flag.StringVar(&apiserverEndpoint, "apiserver-endpoint", "https://localhost:6443", "")
	flag.Parse()

	clientID, ok := os.LookupEnv("CLIENT_ID")
	if !ok {
		log.Fatalln("env CLIENT_ID not set")
	}
	clientSecret, ok := os.LookupEnv("CLIENT_SECRET")
	if !ok {
		log.Fatalln("env CLIENT_SECRET not set")
	}
	cookieStoreAuthKey, ok = os.LookupEnv("COOKIE_STORE_AUTH_KEY")
	if ok {
		if len(cookieStoreAuthKey) != 32 {
			log.Fatalln("COOKIE_STORE_AUTH_KEY is not 32 bytes")
		}
	} else {
		log.Fatalln("env COOKIE_STORE_AUTH_KEY not set")
	}
	cookieStoreEncryptionKey, ok = os.LookupEnv("COOKIE_STORE_ENCRYPTION_KEY")
	if ok {
		if len(cookieStoreEncryptionKey) != 32 {
			log.Fatalln("COOKIE_STORE_ENCRYPTION_KEY is not 32 bytes")
		}
	} else {
		log.Fatalln("env COOKIE_STORE_ENCRYPTION_KEY not set")
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
	namespace := fmt.Sprintf("spotify-%s", spotifyUsername)
	sa, err := clientset.CoreV1().ServiceAccounts(namespace).Get(context.TODO(), spotifyUsername, metav1.GetOptions{})
	if err == nil {
		log.Printf("Got serviceaccount: %s\n", sa.ObjectMeta.Name)
		if len(sa.Secrets) == 0 {
			log.Printf("No secrets found!")
		} else {
			log.Printf("sa secret: %s\n", sa.Secrets[0].Name)
		}
	} else {
		log.Printf("%s is not authenticated\n", spotifyUsername)
	}
	secret, err := clientset.CoreV1().Secrets(namespace).Get(context.TODO(), sa.Secrets[0].Name, metav1.GetOptions{})
	if err == nil {
		kc := `kind: Config
apiVersion: v1
preferences: {}
clusters:
- cluster:
    server: %s
    certificate-authority-data: %s
  name: dj-kubelet
users:
- name: user
  user:
    token: %s
contexts:
- context:
    cluster: dj-kubelet
    namespace: %s
    user: user
  name: user@dj-kubelet
current-context: user@dj-kubelet
`
		return fmt.Sprintf(kc, apiserver, base64.StdEncoding.EncodeToString(secret.Data["ca.crt"]), secret.Data["token"], namespace)
	} else {
		log.Printf("%+v\n", err)
	}
	return "NO Kubeconfig"
}

func createNamespace(token *oauth2.Token, spotifyUsername string) string {
	namespace := fmt.Sprintf("spotify-%s", spotifyUsername)

	// Create the namespace if it doesn't exist
	_, err := clientset.CoreV1().Namespaces().Create(context.TODO(), &apiv1.Namespace{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Namespace",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
			Labels: map[string]string{
				"dj-kubelet.com/managed": "true",
			},
		},
	}, metav1.CreateOptions{})
	if err == nil {
		log.Printf("Created namespace: %s\n", namespace)
	} else {
		log.Printf("%+v\n", err)
	}

	// Create ServiceAccount
	_, err = clientset.CoreV1().ServiceAccounts(namespace).Create(context.TODO(), &apiv1.ServiceAccount{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ServiceAccount",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: spotifyUsername,
		},
	}, metav1.CreateOptions{})
	if err == nil {
		log.Printf("Created serviceaccount: %s\n", namespace)
	} else {
		log.Printf("%+v\n", err)
	}

	// Create ClusterRoleBinding
	_, err = clientset.RbacV1().ClusterRoleBindings().Create(context.TODO(), &rbacv1.ClusterRoleBinding{
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
			Namespace: namespace,
		}},
	}, metav1.CreateOptions{})
	if err == nil {
		log.Printf("Created clusterrolebinding: %s\n", "dj-kubelet:"+spotifyUsername)
	} else {
		log.Printf("%+v\n", err)
	}

	// Create RoleBinding
	_, err = clientset.RbacV1().RoleBindings(namespace).Create(context.TODO(), &rbacv1.RoleBinding{
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
			Namespace: namespace,
		}},
	}, metav1.CreateOptions{})
	if err == nil {
		log.Printf("Created rolebinding: %s\n", "dj-kubelet:"+spotifyUsername)
	} else {
		log.Printf("%+v\n", err)
	}
	return namespace
}

func copyDjControllerDeployment(namespace string) {
	var templateNamespace = "dj-controller"
	var templateDeployment = "dj-controller"

	_, err := clientset.CoreV1().ServiceAccounts(namespace).Create(context.TODO(), &apiv1.ServiceAccount{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ServiceAccount",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: "dj-controller",
		},
	}, metav1.CreateOptions{})
	if err == nil {
		log.Printf("Created serviceaccount %s/dj-controller\n", namespace)
	} else {
		log.Printf("Could not create serviceaccount %s/dj-controller: %+v\n", namespace, err)
	}

	// Copy Role dj-controller/dj-controller
	roleTemplate, err := clientset.RbacV1().Roles(templateNamespace).Get(context.TODO(), "dj-controller", metav1.GetOptions{})
	if err == nil {
		log.Printf("Got template role %s/dj-controller\n", templateNamespace)
		log.Printf("dj-controller rules: %+v\n", roleTemplate.Rules)
	} else {
		log.Printf("Could not get template role %s/dj-controller: %+v\n", templateNamespace, err)
	}
	_, err = clientset.RbacV1().Roles(namespace).Create(context.TODO(), &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name: "dj-controller",
		},
		Rules: roleTemplate.Rules,
	}, metav1.CreateOptions{})
	if err == nil {
		log.Printf("Created role %s/dj-controller\n", namespace)
	} else {
		log.Printf("Could not create role %s/dj-controller: %+v\n", namespace, err)
	}

	// Bind role to SA
	// Create RoleBinding
	_, err = clientset.RbacV1().RoleBindings(namespace).Create(context.TODO(), &rbacv1.RoleBinding{
		TypeMeta: metav1.TypeMeta{
			Kind:       "RoleBinding",
			APIVersion: "rbac.authorization.k8s.io/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: "dj-controller",
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     "dj-controller",
		},
		Subjects: []rbacv1.Subject{rbacv1.Subject{
			Kind:      "ServiceAccount",
			Name:      "dj-controller",
			Namespace: namespace,
		}},
	}, metav1.CreateOptions{})
	if err == nil {
		log.Printf("Created rolebinding %s/dj-controller\n", namespace)
	} else {
		log.Printf("Could not create rolebinding %s/dj-controller: %+v\n", namespace, err)
	}

	// Copy deployment
	template, err := clientset.AppsV1().Deployments(templateNamespace).Get(context.TODO(), templateDeployment, metav1.GetOptions{})
	if err != nil {
		log.Fatal("Could not get deployment dj-controller template")
	}

	var spec = template.Spec
	one := int32(1)
	spec.Replicas = &one
	_, err = clientset.AppsV1().Deployments(namespace).Create(context.TODO(), &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "dj-controller",
		},
		Spec: spec,
	}, metav1.CreateOptions{})
	if err == nil {
		log.Printf("Created deployment %s/dj-controller\n", namespace)
	} else {
		log.Printf("Could not create deployment %s/dj-controller: %+v\n", namespace, err)
	}
}

func createTokenSecret(token *oauth2.Token, namespace, secretName string) {
	// Create new oauth secret
	s := apiv1.Secret{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Secret",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: namespace,
			Labels: map[string]string{
				"dj-kubelet.com/oauth-refresher": "spotify",
			},
		},
		Data: map[string][]byte{
			"access_token":  []byte(token.AccessToken),
			"refresh_token": []byte(token.RefreshToken),
			"expiry":        []byte(token.Expiry.Format(time.RFC3339)),
			"updated":       []byte(time.Now().Format(time.RFC3339)),
		},
		Type: "Opaque",
	}

	secrets := clientset.CoreV1().Secrets(namespace)
	_, err := secrets.Create(context.TODO(), &s, metav1.CreateOptions{})
	if err == nil {
		log.Printf("Created secret %s/%s", namespace, secretName)
	} else {
		log.Printf("%+v\n", err)
	}
}

func health(c echo.Context) error {
	return c.String(http.StatusOK, "ok")
}

type User struct {
	Name       string `json:"name"`
	Kubeconfig string `json:"kubeconfig"`
	Error      bool   `json:"error"`
}

type ErrorResponse struct {
	Status string `json:"status"`
	Error  bool   `json:"error"`
}

func user(c echo.Context) error {
	sess, _ := session.Get("session", c)
	username := sess.Values["username"]
	if username == nil {
		return c.JSON(http.StatusUnauthorized, &ErrorResponse{
			Error:  true,
			Status: http.StatusText(http.StatusUnauthorized),
		})
	}

	// TODO Verify namespace created
	kubeconfig := getKubeconfig(username.(string), apiserverEndpoint)
	u := &User{
		Name:       username.(string),
		Kubeconfig: kubeconfig,
		Error:      false,
	}
	return c.JSON(http.StatusOK, u)
}

func callback(c echo.Context) error {
	username, token, err := sauther.Callback(c)
	if err != nil {
		log.Panic(err)
		c.Error(err)
	}

	//go func() {
	//}()
	ns := createNamespace(token, username)
	copyDjControllerDeployment(ns)
	createTokenSecret(token, ns, secretName)

	return c.Redirect(http.StatusTemporaryRedirect, baseURL)
}

func logout(c echo.Context) error {
	sess, _ := session.Get("session", c)
	sess.Options.MaxAge = -1
	err := sess.Save(c.Request(), c.Response())
	if err != nil {
		log.Fatal("failed to delete session", err)
	}
	return c.JSONBlob(http.StatusOK, []byte("{\"ok\": true}"))
}

func main() {
	setup()

	e := echo.New()

	cookieStore := &sessions.CookieStore{
		Codecs: securecookie.CodecsFromPairs([]byte(cookieStoreAuthKey), []byte(cookieStoreEncryptionKey)),
		Options: &sessions.Options{
			Path:     "/",
			MaxAge:   86400 * 1,
			Secure:   true,
			HttpOnly: true,
		},
	}
	cookieStore.MaxAge(cookieStore.Options.MaxAge)

	e.Use(session.Middleware(cookieStore))
	e.Use(middleware.Logger())
	//e.Use(middleware.Recover())

	e.Static("/static", "static")
	e.File("/", "static/index.html")
	e.GET("/user", user)
	e.GET("/health", health)
	e.GET("/login/spotify", sauther.Auth)
	e.GET("/callback", callback)
	e.GET("/logout", logout)

	e.Logger.Fatal(e.StartTLS(port, certFile, keyFile))
}
