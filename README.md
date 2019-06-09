# Spotify oauther

Runs a web server that will act as your oauth client. An access token is
written to file or to a kubernetes secret. The token will be refreshed before
expiring.

```bash
kubectl create namespace oauther
kubectl -n oauther apply -f ./secret.yaml

cfssl selfsign localhost <(cfssl print-defaults csr) | cfssljson -bare server
kubectl -n oauther create secret tls server-tls --cert=./server.pem --key=./server-key.pem

kubectl apply -f ./rbac.yaml
kubectl -n oauther apply -f deployment.yaml

docker build -t spotify-oauther .

# Load the image into your kubernetes environment.
# If using minikube attach to it's docker daemon before building.
kind load docker-image spotify-oauther

kubectl get pods -n oauther
kubectl -n oauther port-forward $(kubectl -n oauther get pods -oname) 8443:8443
```

Authenticate to the oauther by visiting https://localhost:8443/auth
