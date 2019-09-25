# dj-kubelet Console

The dj-kubelet console is what users interact with and log in to create their dj-kubelet namespaces.

```bash
kubectl create namespace console
kubectl -n console apply -f ./secret.yaml

cfssl selfsign localhost <(cfssl print-defaults csr) | cfssljson -bare server
kubectl -n console create secret tls server-tls --cert=./server.pem --key=./server-key.pem

kubectl apply -f ./rbac.yaml
kubectl apply -f ./roles.yaml
kubectl -n console apply -f ./deployment.yaml

# If using minikube: Attach to the minikube docker daemon before building.
# eval $(minikube docker-env)

docker build -t djkubelet/console .

# If using kind: Load the build image into kind after building.
# kind load docker-image djkubelet/console

kubectl get pods -n console
kubectl -n console port-forward $(kubectl -n console get pods -oname) 8443:8443
```
