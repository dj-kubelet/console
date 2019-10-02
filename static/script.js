
function selectAndCopyKubeconfig() {
  var kc = document.getElementById("kubeconfig");
  kc.select();
  navigator.clipboard.writeText(kc.textContent).then(function() {
    console.log('Copy successful!');
  }, function(err) {
    console.error('Could not copy text: ', err);
  });
}
