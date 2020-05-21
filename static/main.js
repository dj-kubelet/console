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
                    if (data.ok === true) {
                        app.authed = false;
                    }
                });
        },
        copyKubeconfig: function() {
            navigator.clipboard.writeText(app.user.kubeconfig).catch(error => {
                console.error('Could not copy to clipboard: ', error);
            });
        }
    },
    created: function() {
        fetch("/user")
            .then(stream => stream.json())
            .then(function(data) {
                if (data.error === false && "name" in data) {
                    app.authed = true;
                    app.user = data;
                }
            });
    }
});
