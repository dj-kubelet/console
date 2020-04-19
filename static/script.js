
function selectAndCopyKubeconfig() {
  var kc = document.getElementById("kubeconfig");
  kc.select();
  navigator.clipboard.writeText(kc.textContent).then(function() {
    console.log('Copy successful!');
  }, function(err) {
    console.error('Could not copy text: ', err);
  });
}

function getUser() {
  return fetch("/user")
  .then(function(response) {
    return response.json();
  });
}

if (location.hash == "#!authed") {
  getUser().then(function(user) {
    document.querySelector(".window-content").innerHTML = `<p>Nice to have you here ${user.name}!<br>Let's rock and roll!</p>
<textarea id="kubeconfig" cols=80 rows=20 spellcheck="false" class="code">${user.kubeconfig}</textarea>
<p><a style="text-decoration: underline; cursor: pointer;" onclick="selectAndCopyKubeconfig()">Copy Kubeconfig</a></p>`;
  });
}
