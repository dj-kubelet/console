# dj-kubelet Console

The dj-kubelet console is what users interact with and log in to create their dj-kubelet namespaces.

```bash
# Build and load image into kind nodes
docker build -t console .
kind load docker-image console:latest

kubectl create namespace console
cfssl selfsign localhost <(cfssl print-defaults csr) | cfssljson -bare development/server

kubectl apply -k ./development

kubectl get pods -n console
kubectl -n console port-forward $(kubectl -n console get pods -oname) 8443:8443
```
