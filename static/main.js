var app = new Vue({
    el: '#app',
    data: {
        authed: false,
        user: {}
    },
    methods: {
        login: function() {
            window.location.href = "/login/spotify";
        },
        logout: function() {
          fetch("/logout")
              .then(stream => stream.json())
              .then(function(data) {
                  if (data.ok === true){
                    app.authed = false;
                  }
              });
        },
        selectAndCopyKubeconfig: function() {
            var kc = document.getElementById("kubeconfig");
            //kc.select();
            navigator.clipboard.writeText(kc.textContent).then(function() {
                console.log('Copy successful!');
            }, function(err) {
                console.error('Could not copy text: ', err);
            });
        }
    },
    created: function() {
        fetch("/user")
            .then(stream => stream.json())
            .then(function(data) {
            if (data.error === false && "name" in data){
                app.authed = true;
                app.user = data;
                }
            });
    }
});
