package user

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/artifacthub/hub/cmd/hub/handlers/helpers"
	"github.com/artifacthub/hub/internal/hub"
	"github.com/artifacthub/hub/internal/tests"
	"github.com/artifacthub/hub/internal/user"
	"github.com/go-chi/chi"
	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	zerolog.SetGlobalLevel(zerolog.Disabled)
	os.Exit(m.Run())
}

func TestBasicAuth(t *testing.T) {
	hw := newHandlersWrapper()
	hw.cfg.Set("server.basicAuth.enabled", true)
	hw.cfg.Set("server.basicAuth.username", "test")
	hw.cfg.Set("server.basicAuth.password", "test")

	t.Run("without basic auth credentials", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		r, _ := http.NewRequest("GET", "/", nil)
		hw.h.BasicAuth(http.HandlerFunc(testsOK)).ServeHTTP(w, r)
		resp := w.Result()
		defer resp.Body.Close()

		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("with basic auth credentials", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		r, _ := http.NewRequest("GET", "/", nil)
		r.SetBasicAuth("test", "test")
		hw.h.BasicAuth(http.HandlerFunc(testsOK)).ServeHTTP(w, r)
		resp := w.Result()
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})
}

func TestCheckAvailability(t *testing.T) {
	t.Run("invalid input", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		r, _ := http.NewRequest("HEAD", "/?v=value", nil)
		rctx := &chi.Context{
			URLParams: chi.RouteParams{
				Keys:   []string{"resourceKind"},
				Values: []string{"invalid"},
			},
		}
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))

		hw := newHandlersWrapper()
		hw.um.On("CheckAvailability", r.Context(), "invalid", "value").
			Return(false, hub.ErrInvalidInput)
		hw.h.CheckAvailability(w, r)
		resp := w.Result()
		defer resp.Body.Close()
		h := resp.Header

		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		assert.Equal(t, helpers.BuildCacheControlHeader(0), h.Get("Cache-Control"))
		hw.um.AssertExpectations(t)
	})

	t.Run("valid input", func(t *testing.T) {
		t.Run("check availability succeeded", func(t *testing.T) {
			testCases := []struct {
				resourceKind string
				available    bool
			}{
				{
					"userAlias",
					true,
				},
			}
			for _, tc := range testCases {
				tc := tc
				t.Run(fmt.Sprintf("resource kind: %s", tc.resourceKind), func(t *testing.T) {
					t.Parallel()
					w := httptest.NewRecorder()
					r, _ := http.NewRequest("HEAD", "/?v=value", nil)
					rctx := &chi.Context{
						URLParams: chi.RouteParams{
							Keys:   []string{"resourceKind"},
							Values: []string{tc.resourceKind},
						},
					}
					r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))

					hw := newHandlersWrapper()
					hw.um.On("CheckAvailability", r.Context(), tc.resourceKind, "value").Return(tc.available, nil)
					hw.h.CheckAvailability(w, r)
					resp := w.Result()
					defer resp.Body.Close()
					h := resp.Header

					if tc.available {
						assert.Equal(t, http.StatusNotFound, resp.StatusCode)
					} else {
						assert.Equal(t, http.StatusNoContent, resp.StatusCode)
					}
					assert.Equal(t, helpers.BuildCacheControlHeader(0), h.Get("Cache-Control"))
					hw.um.AssertExpectations(t)
				})
			}
		})

		t.Run("check availability failed", func(t *testing.T) {
			t.Parallel()
			w := httptest.NewRecorder()
			r, _ := http.NewRequest("HEAD", "/?v=value", nil)
			rctx := &chi.Context{
				URLParams: chi.RouteParams{
					Keys:   []string{"resourceKind"},
					Values: []string{"userAlias"},
				},
			}
			r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))

			hw := newHandlersWrapper()
			hw.um.On("CheckAvailability", r.Context(), "userAlias", "value").Return(false, tests.ErrFakeDB)
			hw.h.CheckAvailability(w, r)
			resp := w.Result()
			defer resp.Body.Close()

			assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
			hw.um.AssertExpectations(t)
		})
	})
}

func TestGetProfile(t *testing.T) {
	t.Run("error getting profile", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		r, _ := http.NewRequest("GET", "/", nil)
		r = r.WithContext(context.WithValue(r.Context(), hub.UserIDKey, "userID"))

		hw := newHandlersWrapper()
		hw.um.On("GetProfileJSON", r.Context()).Return(nil, tests.ErrFakeDB)
		hw.h.GetProfile(w, r)
		resp := w.Result()
		defer resp.Body.Close()

		assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
		hw.um.AssertExpectations(t)
	})

	t.Run("profile get succeeded", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		r, _ := http.NewRequest("GET", "/", nil)
		r = r.WithContext(context.WithValue(r.Context(), hub.UserIDKey, "userID"))

		hw := newHandlersWrapper()
		hw.um.On("GetProfileJSON", r.Context()).Return([]byte("dataJSON"), nil)
		hw.h.GetProfile(w, r)
		resp := w.Result()
		defer resp.Body.Close()
		h := resp.Header
		data, _ := ioutil.ReadAll(resp.Body)

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "application/json", h.Get("Content-Type"))
		assert.Equal(t, helpers.BuildCacheControlHeader(0), h.Get("Cache-Control"))
		assert.Equal(t, []byte("dataJSON"), data)
		hw.um.AssertExpectations(t)
	})
}

func TestInjectUserID(t *testing.T) {
	checkUserID := func(expectedUserID interface{}) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if expectedUserID == nil {
				assert.Nil(t, r.Context().Value(hub.UserIDKey))
			} else {
				assert.Equal(t, expectedUserID, r.Context().Value(hub.UserIDKey).(string))
			}
		}
	}

	t.Run("session cookie not provided", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		r, _ := http.NewRequest("GET", "/", nil)

		hw := newHandlersWrapper()
		hw.h.InjectUserID(checkUserID(nil)).ServeHTTP(w, r)
		resp := w.Result()
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("invalid session cookie provided", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		r, _ := http.NewRequest("GET", "/", nil)
		r.AddCookie(&http.Cookie{
			Name:  sessionCookieName,
			Value: "invalidValue",
		})

		hw := newHandlersWrapper()
		hw.h.InjectUserID(checkUserID(nil)).ServeHTTP(w, r)
		resp := w.Result()
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("error checking session", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		r, _ := http.NewRequest("GET", "/", nil)

		hw := newHandlersWrapper()
		encodedSessionID, _ := hw.h.sc.Encode(sessionCookieName, []byte("sessionID"))
		r.AddCookie(&http.Cookie{
			Name:  sessionCookieName,
			Value: encodedSessionID,
		})
		hw.um.On("CheckSession", r.Context(), mock.Anything, mock.Anything).
			Return(nil, tests.ErrFakeDB)
		hw.h.InjectUserID(checkUserID(nil)).ServeHTTP(w, r)
		resp := w.Result()
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		hw.um.AssertExpectations(t)
	})

	t.Run("invalid session provided", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		r, _ := http.NewRequest("GET", "/", nil)

		hw := newHandlersWrapper()
		hw.um.On("CheckSession", r.Context(), mock.Anything, mock.Anything).
			Return(&hub.CheckSessionOutput{UserID: "", Valid: false}, nil)

		encodedSessionID, _ := hw.h.sc.Encode(sessionCookieName, []byte("sessionID"))
		r.AddCookie(&http.Cookie{
			Name:  sessionCookieName,
			Value: encodedSessionID,
		})
		hw.h.InjectUserID(checkUserID(nil)).ServeHTTP(w, r)
		resp := w.Result()
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		hw.um.AssertExpectations(t)
	})

	t.Run("inject user id succeeded", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		r, _ := http.NewRequest("GET", "/", nil)

		hw := newHandlersWrapper()
		hw.um.On("CheckSession", r.Context(), mock.Anything, mock.Anything).
			Return(&hub.CheckSessionOutput{UserID: "userID", Valid: true}, nil)
		encodedSessionID, _ := hw.h.sc.Encode(sessionCookieName, []byte("sessionID"))
		r.AddCookie(&http.Cookie{
			Name:  sessionCookieName,
			Value: encodedSessionID,
		})
		hw.h.InjectUserID(checkUserID("userID")).ServeHTTP(w, r)
		resp := w.Result()
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		hw.um.AssertExpectations(t)
	})
}

func TestLogin(t *testing.T) {
	t.Run("invalid", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		body := strings.NewReader(`{"email": "email" ...`)
		r, _ := http.NewRequest("POST", "/", body)

		hw := newHandlersWrapper()
		hw.h.Login(w, r)
		resp := w.Result()
		defer resp.Body.Close()

		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("credentials not provided", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		body := strings.NewReader(`{}`)
		r, _ := http.NewRequest("POST", "/", body)

		hw := newHandlersWrapper()
		hw.um.On("CheckCredentials", r.Context(), "", "").Return(nil, hub.ErrInvalidInput)
		hw.h.Login(w, r)
		resp := w.Result()
		defer resp.Body.Close()

		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("error checking credentials", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		body := strings.NewReader(`{"email": "email", "password": "pass"}`)
		r, _ := http.NewRequest("POST", "/", body)

		hw := newHandlersWrapper()
		hw.um.On("CheckCredentials", r.Context(), "email", "pass").Return(nil, tests.ErrFakeDB)
		hw.h.Login(w, r)
		resp := w.Result()
		defer resp.Body.Close()

		assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
		hw.um.AssertExpectations(t)
	})

	t.Run("invalid credentials provided", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		body := strings.NewReader(`{"email": "email", "password": "pass2"}`)
		r, _ := http.NewRequest("POST", "/", body)

		hw := newHandlersWrapper()
		hw.um.On("CheckCredentials", r.Context(), "email", "pass2").
			Return(&hub.CheckCredentialsOutput{Valid: false, UserID: ""}, nil)
		hw.h.Login(w, r)
		resp := w.Result()
		defer resp.Body.Close()

		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		hw.um.AssertExpectations(t)
	})

	t.Run("error registering session", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		body := strings.NewReader(`{"email": "email", "password": "pass"}`)
		r, _ := http.NewRequest("POST", "/", body)

		hw := newHandlersWrapper()
		hw.um.On("CheckCredentials", r.Context(), "email", "pass").
			Return(&hub.CheckCredentialsOutput{Valid: true, UserID: "userID"}, nil)
		hw.um.On("RegisterSession", r.Context(), &hub.Session{UserID: "userID"}).
			Return(nil, tests.ErrFakeDB)
		hw.h.Login(w, r)
		resp := w.Result()
		defer resp.Body.Close()

		assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
		hw.um.AssertExpectations(t)
	})

	t.Run("login succeeded", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		body := strings.NewReader(`{"email": "email", "password": "pass"}`)
		r, _ := http.NewRequest("POST", "/", body)

		hw := newHandlersWrapper()
		hw.um.On("CheckCredentials", r.Context(), "email", "pass").
			Return(&hub.CheckCredentialsOutput{Valid: true, UserID: "userID"}, nil)
		hw.um.On("RegisterSession", r.Context(), &hub.Session{UserID: "userID"}).
			Return([]byte("sessionID"), nil)
		hw.h.Login(w, r)
		resp := w.Result()
		defer resp.Body.Close()

		assert.Equal(t, http.StatusNoContent, resp.StatusCode)
		require.Len(t, resp.Cookies(), 1)
		cookie := resp.Cookies()[0]
		assert.Equal(t, sessionCookieName, cookie.Name)
		assert.Equal(t, "/", cookie.Path)
		assert.True(t, cookie.HttpOnly)
		assert.False(t, cookie.Secure)
		var sessionID []byte
		err := hw.h.sc.Decode(sessionCookieName, cookie.Value, &sessionID)
		require.NoError(t, err)
		assert.Equal(t, []byte("sessionID"), sessionID)
		hw.um.AssertExpectations(t)
	})
}

func TestLogout(t *testing.T) {
	t.Run("invalid or no session cookie provided", func(t *testing.T) {
		testCases := []struct {
			description string
			cookie      *http.Cookie
		}{
			{
				"invalid session cookie provided",
				nil,
			},
			{
				"no session cookie provided",
				&http.Cookie{
					Name:  sessionCookieName,
					Value: "invalidValue",
				},
			},
		}
		for _, tc := range testCases {
			tc := tc
			t.Run(tc.description, func(t *testing.T) {
				t.Parallel()
				w := httptest.NewRecorder()
				r, _ := http.NewRequest("GET", "/", nil)
				if tc.cookie != nil {
					r.AddCookie(tc.cookie)
				}

				hw := newHandlersWrapper()
				hw.h.Logout(w, r)
				resp := w.Result()
				defer resp.Body.Close()

				assert.Equal(t, http.StatusNoContent, resp.StatusCode)
				require.Len(t, resp.Cookies(), 1)
				cookie := resp.Cookies()[0]
				assert.Equal(t, sessionCookieName, cookie.Name)
				assert.True(t, cookie.Expires.Before(time.Now().Add(-24*time.Hour)))
			})
		}
	})

	t.Run("valid session cookie provided", func(t *testing.T) {
		testCases := []struct {
			description string
			err         interface{}
		}{
			{
				"session deleted successfully",
				nil,
			},
			{
				"error deleting session",
				tests.ErrFakeDB,
			},
		}
		for _, tc := range testCases {
			tc := tc
			t.Run(tc.description, func(t *testing.T) {
				t.Parallel()
				w := httptest.NewRecorder()
				r, _ := http.NewRequest("GET", "/", nil)

				hw := newHandlersWrapper()
				hw.um.On("DeleteSession", r.Context(), []byte("sessionID")).Return(tc.err)
				encodedSessionID, _ := hw.h.sc.Encode(sessionCookieName, []byte("sessionID"))
				r.AddCookie(&http.Cookie{
					Name:  sessionCookieName,
					Value: encodedSessionID,
				})
				hw.h.Logout(w, r)
				resp := w.Result()
				defer resp.Body.Close()

				assert.Equal(t, http.StatusNoContent, resp.StatusCode)
				require.Len(t, resp.Cookies(), 1)
				cookie := resp.Cookies()[0]
				assert.Equal(t, sessionCookieName, cookie.Name)
				assert.True(t, cookie.Expires.Before(time.Now().Add(-24*time.Hour)))
				hw.um.AssertExpectations(t)
			})
		}
	})
}

func TestOauthCallback(t *testing.T) {
	t.Run("invalid oauth code or state", func(t *testing.T) {
		state := &OauthState{
			Random:      "abcd",
			RedirectURL: "/",
		}

		testCases := []struct {
			description string
			url         string
			cookie      *http.Cookie
		}{
			{
				"oauth code not provided",
				"/",
				nil,
			},
			{
				"oauth state not provided",
				"/?code=1234",
				nil,
			},
			{
				"state cookie not provided",
				"/?code=1234&state=" + state.String(),
				nil,
			},
			{
				"invalid state cookie",
				"/?code=1234&state=" + state.String(),
				&http.Cookie{
					Name:  oauthStateCookieName,
					Value: "something not expected",
				},
			},
		}
		for _, tc := range testCases {
			tc := tc
			t.Run(tc.description, func(t *testing.T) {
				t.Parallel()
				w := httptest.NewRecorder()
				r, _ := http.NewRequest("GET", tc.url, nil)
				if tc.cookie != nil {
					r.AddCookie(tc.cookie)
				}

				hw := newHandlersWrapper()
				hw.h.OauthCallback(w, r)
				resp := w.Result()
				defer resp.Body.Close()

				assert.Equal(t, http.StatusSeeOther, resp.StatusCode)
				redirectURL, err := resp.Location()
				require.NoError(t, err)
				assert.Equal(t, oauthFailedURL, redirectURL.String())
			})
		}
	})
}

func TestOauthRedirect(t *testing.T) {
	t.Parallel()
	w := httptest.NewRecorder()
	r, _ := http.NewRequest("GET", "/", nil)
	rctx := &chi.Context{
		URLParams: chi.RouteParams{
			Keys:   []string{"provider"},
			Values: []string{"github"},
		},
	}
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))

	hw := newHandlersWrapper()
	hw.h.OauthRedirect(w, r)
	resp := w.Result()
	defer resp.Body.Close()

	require.Len(t, resp.Cookies(), 1)
	assert.Equal(t, oauthStateCookieName, resp.Cookies()[0].Name)
	assert.NotEmpty(t, resp.Cookies()[0].Value)
	assert.Equal(t, "/", resp.Cookies()[0].Path)
	assert.True(t, resp.Cookies()[0].HttpOnly)
	assert.False(t, resp.Cookies()[0].Secure)
	assert.Equal(t, http.StatusSeeOther, resp.StatusCode)
	state := &OauthState{
		Random:      resp.Cookies()[0].Value,
		RedirectURL: "/",
	}
	expectedRedirectURL := hw.h.oauthConfig["github"].AuthCodeURL(state.String())
	redirectURL, err := resp.Location()
	require.NoError(t, err)
	assert.Equal(t, expectedRedirectURL, redirectURL.String())
}

func TestRegisterPasswordResetCode(t *testing.T) {
	t.Run("invalid input", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		body := strings.NewReader(`email`)
		r, _ := http.NewRequest("POST", "/", body)

		hw := newHandlersWrapper()
		hw.h.RegisterPasswordResetCode(w, r)
		resp := w.Result()
		defer resp.Body.Close()

		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		hw.um.AssertExpectations(t)
	})

	t.Run("register password reset code failed", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		body := strings.NewReader(`{"email": "email"}`)
		r, _ := http.NewRequest("POST", "/", body)

		hw := newHandlersWrapper()
		hw.um.On("RegisterPasswordResetCode", r.Context(), "email", "baseURL").Return(tests.ErrFakeDB)
		hw.h.RegisterPasswordResetCode(w, r)
		resp := w.Result()
		defer resp.Body.Close()

		assert.Equal(t, http.StatusCreated, resp.StatusCode)
		hw.um.AssertExpectations(t)
	})

	t.Run("register password reset code succeeded", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		body := strings.NewReader(`{"email": "email"}`)
		r, _ := http.NewRequest("POST", "/", body)

		hw := newHandlersWrapper()
		hw.um.On("RegisterPasswordResetCode", r.Context(), "email", "baseURL").Return(tests.ErrFakeDB)
		hw.h.RegisterPasswordResetCode(w, r)
		resp := w.Result()
		defer resp.Body.Close()

		assert.Equal(t, http.StatusCreated, resp.StatusCode)
		hw.um.AssertExpectations(t)
	})
}

func TestRegisterUser(t *testing.T) {
	t.Run("no user provided", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		r, _ := http.NewRequest("POST", "/", strings.NewReader(""))

		hw := newHandlersWrapper()
		hw.h.RegisterUser(w, r)
		resp := w.Result()
		defer resp.Body.Close()

		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("invalid user provided", func(t *testing.T) {
		testCases := []struct {
			description string
			userJSON    string
			umErr       error
		}{
			{
				"invalid json",
				"-",
				nil,
			},
			{
				"missing password",
				`{"alias": "alias", "email": "email"}`,
				nil,
			},
			{
				"missing alias",
				`{"email": "email", "password": "password"}`,
				hub.ErrInvalidInput,
			},
			{
				"missing email",
				`{"alias": "alias", "password": "password"}`,
				hub.ErrInvalidInput,
			},
		}
		for _, tc := range testCases {
			tc := tc
			t.Run(tc.description, func(t *testing.T) {
				t.Parallel()
				w := httptest.NewRecorder()
				r, _ := http.NewRequest("POST", "/", strings.NewReader(tc.userJSON))

				hw := newHandlersWrapper()
				if tc.umErr != nil {
					hw.um.On("RegisterUser", r.Context(), mock.Anything, "baseURL").Return(tc.umErr)
				}
				hw.h.RegisterUser(w, r)
				resp := w.Result()
				defer resp.Body.Close()

				assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
				hw.um.AssertExpectations(t)
			})
		}
	})

	t.Run("valid user provided", func(t *testing.T) {
		userJSON := `
		{
			"alias": "alias",
			"first_name": "first_name",
			"last_name": "last_name",
			"email": "email",
			"password": "password"
		}
		`
		u := &hub.User{}
		_ = json.Unmarshal([]byte(userJSON), &u)

		testCases := []struct {
			description        string
			umErr              error
			expectedStatusCode int
		}{
			{
				"registration succeeded",
				nil,
				http.StatusCreated,
			},
			{
				"registration failed",
				tests.ErrFakeDB,
				http.StatusInternalServerError,
			},
		}
		for _, tc := range testCases {
			tc := tc
			t.Run(tc.description, func(t *testing.T) {
				t.Parallel()
				w := httptest.NewRecorder()
				r, _ := http.NewRequest("POST", "/", strings.NewReader(userJSON))

				hw := newHandlersWrapper()
				hw.um.On("RegisterUser", r.Context(), u, "baseURL").Return(tc.umErr)
				hw.h.RegisterUser(w, r)
				resp := w.Result()
				defer resp.Body.Close()

				assert.Equal(t, tc.expectedStatusCode, resp.StatusCode)
				hw.um.AssertExpectations(t)
			})
		}
	})
}

func TestRequireLogin(t *testing.T) {
	sessionID := []byte("sessionID")

	t.Run("api key based authentication", func(t *testing.T) {
		key := []byte("key")
		keyB64 := base64.StdEncoding.EncodeToString(key)

		t.Run("invalid api key provided", func(t *testing.T) {
			t.Parallel()
			w := httptest.NewRecorder()
			r, _ := http.NewRequest("GET", "/", nil)
			r.Header.Add(APIKeyHeader, "invalidB64")

			hw := newHandlersWrapper()
			hw.h.RequireLogin(http.HandlerFunc(testsOK)).ServeHTTP(w, r)
			resp := w.Result()
			defer resp.Body.Close()

			assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		})

		t.Run("error checking api key", func(t *testing.T) {
			t.Parallel()
			w := httptest.NewRecorder()
			r, _ := http.NewRequest("GET", "/", nil)
			r.Header.Add(APIKeyHeader, keyB64)

			hw := newHandlersWrapper()
			hw.um.On("CheckAPIKey", r.Context(), key).Return(nil, tests.ErrFakeDB)
			hw.h.RequireLogin(http.HandlerFunc(testsOK)).ServeHTTP(w, r)
			resp := w.Result()
			defer resp.Body.Close()

			assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
			hw.um.AssertExpectations(t)
		})

		t.Run("invalid api key provided", func(t *testing.T) {
			t.Parallel()
			w := httptest.NewRecorder()
			r, _ := http.NewRequest("GET", "/", nil)
			r.Header.Add(APIKeyHeader, keyB64)

			hw := newHandlersWrapper()
			hw.um.On("CheckAPIKey", r.Context(), key).
				Return(&hub.CheckAPIKeyOutput{UserID: "", Valid: false}, nil)
			hw.h.RequireLogin(http.HandlerFunc(testsOK)).ServeHTTP(w, r)
			resp := w.Result()
			defer resp.Body.Close()

			assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
			hw.um.AssertExpectations(t)
		})

		t.Run("api key based authentication succeeded", func(t *testing.T) {
			t.Parallel()
			w := httptest.NewRecorder()
			r, _ := http.NewRequest("GET", "/", nil)
			r.Header.Add(APIKeyHeader, keyB64)

			hw := newHandlersWrapper()
			hw.um.On("CheckAPIKey", r.Context(), key).
				Return(&hub.CheckAPIKeyOutput{UserID: "userID", Valid: true}, nil)
			hw.h.RequireLogin(http.HandlerFunc(testsOK)).ServeHTTP(w, r)
			resp := w.Result()
			defer resp.Body.Close()

			assert.Equal(t, http.StatusOK, resp.StatusCode)
			hw.um.AssertExpectations(t)
		})
	})

	t.Run("session cookie based authentication", func(t *testing.T) {
		t.Run("invalid session cookie provided", func(t *testing.T) {
			t.Parallel()
			w := httptest.NewRecorder()
			r, _ := http.NewRequest("GET", "/", nil)
			r.AddCookie(&http.Cookie{
				Name:  sessionCookieName,
				Value: "invalidValue",
			})

			hw := newHandlersWrapper()
			hw.h.RequireLogin(http.HandlerFunc(testsOK)).ServeHTTP(w, r)
			resp := w.Result()
			defer resp.Body.Close()

			assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		})

		t.Run("error checking session", func(t *testing.T) {
			t.Parallel()
			w := httptest.NewRecorder()
			r, _ := http.NewRequest("GET", "/", nil)

			hw := newHandlersWrapper()
			hw.um.On("CheckSession", r.Context(), sessionID, sessionDuration).
				Return(nil, tests.ErrFakeDB)
			encodedSessionID, _ := hw.h.sc.Encode(sessionCookieName, sessionID)
			r.AddCookie(&http.Cookie{
				Name:  sessionCookieName,
				Value: encodedSessionID,
			})
			hw.h.RequireLogin(http.HandlerFunc(testsOK)).ServeHTTP(w, r)
			resp := w.Result()
			defer resp.Body.Close()

			assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
			hw.um.AssertExpectations(t)
		})

		t.Run("invalid session provided", func(t *testing.T) {
			t.Parallel()
			w := httptest.NewRecorder()
			r, _ := http.NewRequest("GET", "/", nil)

			hw := newHandlersWrapper()
			hw.um.On("CheckSession", r.Context(), sessionID, sessionDuration).
				Return(&hub.CheckSessionOutput{UserID: "", Valid: false}, nil)
			encodedSessionID, _ := hw.h.sc.Encode(sessionCookieName, sessionID)
			r.AddCookie(&http.Cookie{
				Name:  sessionCookieName,
				Value: encodedSessionID,
			})
			hw.h.RequireLogin(http.HandlerFunc(testsOK)).ServeHTTP(w, r)
			resp := w.Result()
			defer resp.Body.Close()

			assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
			hw.um.AssertExpectations(t)
		})

		t.Run("session cookie based authentication succeeded", func(t *testing.T) {
			t.Parallel()
			w := httptest.NewRecorder()
			r, _ := http.NewRequest("GET", "/", nil)

			hw := newHandlersWrapper()
			hw.um.On("CheckSession", r.Context(), sessionID, sessionDuration).
				Return(&hub.CheckSessionOutput{UserID: "userID", Valid: true}, nil)
			encodedSessionID, _ := hw.h.sc.Encode(sessionCookieName, sessionID)
			r.AddCookie(&http.Cookie{
				Name:  sessionCookieName,
				Value: encodedSessionID,
			})
			hw.h.RequireLogin(http.HandlerFunc(testsOK)).ServeHTTP(w, r)
			resp := w.Result()
			defer resp.Body.Close()

			assert.Equal(t, http.StatusOK, resp.StatusCode)
			hw.um.AssertExpectations(t)
		})
	})

	t.Run("no authentication method used", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		r, _ := http.NewRequest("GET", "/", nil)

		hw := newHandlersWrapper()
		hw.h.RequireLogin(http.HandlerFunc(testsOK)).ServeHTTP(w, r)
		resp := w.Result()
		defer resp.Body.Close()

		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})
}

func TestResetPassword(t *testing.T) {
	t.Run("invalid input", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		body := strings.NewReader(`code`)
		r, _ := http.NewRequest("PUT", "/", body)

		hw := newHandlersWrapper()
		hw.h.ResetPassword(w, r)
		resp := w.Result()
		defer resp.Body.Close()

		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		hw.um.AssertExpectations(t)
	})

	t.Run("password reset failed (invalid code)", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		body := strings.NewReader(`{"code": "code", "password": "password"}`)
		r, _ := http.NewRequest("PUT", "/", body)

		hw := newHandlersWrapper()
		hw.um.On("ResetPassword", r.Context(), "code", "password", "baseURL").
			Return(user.ErrInvalidPasswordResetCode)
		hw.h.ResetPassword(w, r)
		resp := w.Result()
		defer resp.Body.Close()

		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		hw.um.AssertExpectations(t)
	})

	t.Run("password reset failed (db error)", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		body := strings.NewReader(`{"code": "code", "password": "password"}`)
		r, _ := http.NewRequest("PUT", "/", body)

		hw := newHandlersWrapper()
		hw.um.On("ResetPassword", r.Context(), "code", "password", "baseURL").Return(tests.ErrFakeDB)
		hw.h.ResetPassword(w, r)
		resp := w.Result()
		defer resp.Body.Close()

		assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
		hw.um.AssertExpectations(t)
	})

	t.Run("password reset succeeded", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		body := strings.NewReader(`{"code": "code", "password": "password"}`)
		r, _ := http.NewRequest("PUT", "/", body)
		r = r.WithContext(context.WithValue(r.Context(), hub.UserIDKey, "userID"))

		hw := newHandlersWrapper()
		hw.um.On("ResetPassword", r.Context(), "code", "password", "baseURL").Return(nil)
		hw.h.ResetPassword(w, r)
		resp := w.Result()
		defer resp.Body.Close()

		assert.Equal(t, http.StatusNoContent, resp.StatusCode)
		hw.um.AssertExpectations(t)
	})
}

func TestUpdatePassword(t *testing.T) {
	t.Run("no old password provided", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		body := strings.NewReader(`{"new": "new"}`)
		r, _ := http.NewRequest("PUT", "/", body)

		hw := newHandlersWrapper()
		hw.um.On("UpdatePassword", r.Context(), "", "new").Return(hub.ErrInvalidInput)
		hw.h.UpdatePassword(w, r)
		resp := w.Result()
		defer resp.Body.Close()

		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		hw.um.AssertExpectations(t)
	})

	t.Run("no new password provided", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		body := strings.NewReader(`{"old": "old"}`)
		r, _ := http.NewRequest("PUT", "/", body)

		hw := newHandlersWrapper()
		hw.um.On("UpdatePassword", r.Context(), "old", "").Return(hub.ErrInvalidInput)
		hw.h.UpdatePassword(w, r)
		resp := w.Result()
		defer resp.Body.Close()

		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		hw.um.AssertExpectations(t)
	})

	t.Run("invalid old password provided", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		body := strings.NewReader(`{"old": "invalid", "new": "new"}`)
		r, _ := http.NewRequest("PUT", "/", body)
		r = r.WithContext(context.WithValue(r.Context(), hub.UserIDKey, "userID"))

		hw := newHandlersWrapper()
		hw.um.On("UpdatePassword", r.Context(), "invalid", "new").
			Return(user.ErrInvalidPassword)
		hw.h.UpdatePassword(w, r)
		resp := w.Result()
		defer resp.Body.Close()

		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		hw.um.AssertExpectations(t)
	})

	t.Run("error updating password", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		body := strings.NewReader(`{"old": "old", "new": "new"}`)
		r, _ := http.NewRequest("PUT", "/", body)
		r = r.WithContext(context.WithValue(r.Context(), hub.UserIDKey, "userID"))

		hw := newHandlersWrapper()
		hw.um.On("UpdatePassword", r.Context(), "old", "new").
			Return(tests.ErrFakeDB)
		hw.h.UpdatePassword(w, r)
		resp := w.Result()
		defer resp.Body.Close()

		assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
		hw.um.AssertExpectations(t)
	})

	t.Run("password updated successfully", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		body := strings.NewReader(`{"old": "old", "new": "new"}`)
		r, _ := http.NewRequest("PUT", "/", body)
		r = r.WithContext(context.WithValue(r.Context(), hub.UserIDKey, "userID"))

		hw := newHandlersWrapper()
		hw.um.On("UpdatePassword", r.Context(), "old", "new").Return(nil)
		hw.h.UpdatePassword(w, r)
		resp := w.Result()
		defer resp.Body.Close()

		assert.Equal(t, http.StatusNoContent, resp.StatusCode)
		hw.um.AssertExpectations(t)
	})
}

func TestUpdateProfile(t *testing.T) {
	userJSON := `{"first_name": "firstname", "last_name": "lastname"}`
	u := &hub.User{}
	_ = json.Unmarshal([]byte(userJSON), &u)

	t.Run("invalid input", func(t *testing.T) {
		testCases := []struct {
			desc     string
			userJSON string
			umErr    error
		}{
			{
				"no user provided",
				"",
				nil,
			},
			{
				"invalid user json",
				"{invalid json",
				nil,
			},
			{
				"alias not provided",
				"{}",
				hub.ErrInvalidInput,
			},
		}
		for _, tc := range testCases {
			tc := tc
			t.Run(tc.desc, func(t *testing.T) {
				t.Parallel()
				w := httptest.NewRecorder()
				r, _ := http.NewRequest("PUT", "/", strings.NewReader(tc.userJSON))

				hw := newHandlersWrapper()
				if tc.umErr != nil {
					hw.um.On("UpdateProfile", r.Context(), mock.Anything).Return(tc.umErr)
				}
				hw.h.UpdateProfile(w, r)
				resp := w.Result()
				defer resp.Body.Close()

				assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
				hw.um.AssertExpectations(t)
			})
		}
	})

	t.Run("error updating profile", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		r, _ := http.NewRequest("PUT", "/", strings.NewReader(userJSON))
		r = r.WithContext(context.WithValue(r.Context(), hub.UserIDKey, "userID"))

		hw := newHandlersWrapper()
		hw.um.On("UpdateProfile", r.Context(), u).Return(tests.ErrFakeDB)
		hw.h.UpdateProfile(w, r)
		resp := w.Result()
		defer resp.Body.Close()

		assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
		hw.um.AssertExpectations(t)
	})

	t.Run("user profile updated successfully", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		r, _ := http.NewRequest("PUT", "/", strings.NewReader(userJSON))
		r = r.WithContext(context.WithValue(r.Context(), hub.UserIDKey, "userID"))

		hw := newHandlersWrapper()
		hw.um.On("UpdateProfile", r.Context(), u).Return(nil)
		hw.h.UpdateProfile(w, r)
		resp := w.Result()
		defer resp.Body.Close()

		assert.Equal(t, http.StatusNoContent, resp.StatusCode)
		hw.um.AssertExpectations(t)
	})
}

func TestVerifyEmail(t *testing.T) {
	testCases := []struct {
		description        string
		response           []interface{}
		expectedStatusCode int
	}{
		{
			"code not provided",
			[]interface{}{false, hub.ErrInvalidInput},
			http.StatusBadRequest,
		},
		{
			"code not verified",
			[]interface{}{false, nil},
			http.StatusGone,
		},
		{
			"code verified",
			[]interface{}{true, nil},
			http.StatusNoContent,
		},
		{
			"database error",
			[]interface{}{false, tests.ErrFakeDB},
			http.StatusInternalServerError,
		},
	}
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.description, func(t *testing.T) {
			t.Parallel()
			w := httptest.NewRecorder()
			r, _ := http.NewRequest("POST", "/", strings.NewReader(`{"code": "1234"}`))

			hw := newHandlersWrapper()
			hw.um.On("VerifyEmail", r.Context(), "1234").Return(tc.response...)
			hw.h.VerifyEmail(w, r)
			resp := w.Result()
			defer resp.Body.Close()

			assert.Equal(t, tc.expectedStatusCode, resp.StatusCode)
			hw.um.AssertExpectations(t)
		})
	}
}

func TestVerifyPasswordResetCode(t *testing.T) {
	t.Run("invalid input", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		body := strings.NewReader(`code`)
		r, _ := http.NewRequest("POST", "/", body)

		hw := newHandlersWrapper()
		hw.h.VerifyPasswordResetCode(w, r)
		resp := w.Result()
		defer resp.Body.Close()

		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		hw.um.AssertExpectations(t)
	})

	t.Run("valid input", func(t *testing.T) {
		testCases := []struct {
			description        string
			err                error
			expectedStatusCode int
		}{
			{
				"valid code",
				nil,
				http.StatusOK,
			},
			{
				"invalid code",
				user.ErrInvalidPasswordResetCode,
				http.StatusGone,
			},
			{
				"database error",
				tests.ErrFakeDB,
				http.StatusInternalServerError,
			},
		}
		for _, tc := range testCases {
			tc := tc
			t.Run(tc.description, func(t *testing.T) {
				t.Parallel()
				w := httptest.NewRecorder()
				r, _ := http.NewRequest("POST", "/", strings.NewReader(`{"code": "code"}`))

				hw := newHandlersWrapper()
				hw.um.On("VerifyPasswordResetCode", r.Context(), "code").Return(tc.err)
				hw.h.VerifyPasswordResetCode(w, r)
				resp := w.Result()
				defer resp.Body.Close()

				assert.Equal(t, tc.expectedStatusCode, resp.StatusCode)
				hw.um.AssertExpectations(t)
			})
		}
	})
}

func testsOK(w http.ResponseWriter, r *http.Request) {}

type handlersWrapper struct {
	cfg *viper.Viper
	um  *user.ManagerMock
	h   *Handlers
}

func newHandlersWrapper() *handlersWrapper {
	cfg := viper.New()
	cfg.Set("server.baseURL", "baseURL")
	cfg.Set("server.oauth.github", map[string]string{})
	um := &user.ManagerMock{}
	h, _ := NewHandlers(context.Background(), um, cfg)

	return &handlersWrapper{
		cfg: cfg,
		um:  um,
		h:   h,
	}
}
