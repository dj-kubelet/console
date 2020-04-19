package auth

import (
	"context"
	"encoding/json"
	"io/ioutil"
	"log"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/spotify"
)

type (
	Auther interface {
		//New(clientID string, clientSecret string, callbackURL string) *Auther
		Auth(c echo.Context) error
		Callback(c echo.Context) (string, *oauth2.Token, error)
	}
	auther struct {
		name string
		conf *oauth2.Config
	}
)

func New(clientID string, clientSecret string, callbackURL string) *auther {
	return &auther{
		name: "sp",
		conf: &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			Scopes: []string{
				// Provide info about which user that's signed in.
				"user-read-private",
				// Not used but could be useful?
				"user-read-currently-playing",
				// To read the current playing song
				"user-read-playback-state",
				// For switching song
				"user-modify-playback-state",
			},
			RedirectURL: callbackURL,
			Endpoint:    spotify.Endpoint,
		},
	}
}

func (a auther) Auth(c echo.Context) error {
	sess, _ := session.Get("session", c)
	state := uuid.New().String()
	sess.Values["spotify-oauth-state"] = state
	err := sess.Save(c.Request(), c.Response())
	if err != nil {
		log.Fatal(err)
		c.Error(err)
	}

	url := a.conf.AuthCodeURL(state, oauth2.SetAuthURLParam("show_dialog", "true"))
	return c.Redirect(http.StatusTemporaryRedirect, url)
}

func (a auther) Callback(c echo.Context) (string, *oauth2.Token, error) {
	sess, _ := session.Get("session", c)
	u := c.Request().URL
	if sess.Values["spotify-oauth-state"] != u.Query().Get("state") {
		log.Println("Invalid state")
		return "", nil, c.String(http.StatusBadRequest, "Invalid state")
	}
	ctx := context.Background()
	token, err := a.conf.Exchange(ctx, u.Query().Get("code"))
	if err != nil {
		log.Fatal(err)
		return "", nil, err
	}
	username, err := a.getSpotifyUsername(token)
	if err != nil {
		log.Fatal(err)
		return "", nil, err
	}
	sess.Values["username"] = username
	sess.Save(c.Request(), c.Response())
	return username, token, nil
}

func (a auther) getSpotifyUsername(token *oauth2.Token) (string, error) {
	ctx := context.Background()
	client := a.conf.Client(ctx, token)
	res, err := client.Get("https://api.spotify.com/v1/me")
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return "", err
	}

	type spotifyMe struct {
		ID string `json:"id"` // id or username
	}

	var me spotifyMe
	err = json.Unmarshal(body, &me)
	if err != nil {
		return "", err
	}
	return me.ID, nil
}
