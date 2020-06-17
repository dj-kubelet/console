# dj-kubelet Console

The dj-kubelet console is what users interact with and log in to create their dj-kubelet namespaces.

```bash
# Build and load image into the dj-kubelet kind nodes
docker build -t console .
kind load docker-image --name dj-kubelet console:latest

kubectl create namespace console
cfssl selfsign localhost <(cfssl print-defaults csr) | cfssljson -bare development/server

kubectl apply -k ./development

kubectl -n console get pods
kubectl -n console logs -lapp=console -f
curl -k https://localhost:30443
# Or kubectl port forward if kind is not doing it.
kubectl -n console port-forward deployment/console 8443:8443
```

## Credit
Huge props to:
- https://github.com/botoxparty/XP.css
- https://github.com/jdan/98.css
- https://github.com/vuejs/vue
- https://win98icons.alexmeub.com/
