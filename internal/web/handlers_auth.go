package web

import (
	"errors"
	"net/http"

	"github.com/kiineld/telemt-panel/internal/store"
)

func (s *server) getLogin(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "login.html", page{Title: "Sign in"})
}

func (s *server) postLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.render(w, http.StatusBadRequest, "login.html", page{Title: "Sign in", Error: "Malformed form."})
		return
	}

	token, _, err := s.Auth.Login(r.Context(), clientIP(r),
		r.PostFormValue("username"), r.PostFormValue("password"))
	if err != nil {
		msg := "Invalid username or password."
		if errors.Is(err, ErrRateLimited) {
			msg = "Too many failed attempts. Wait 15 minutes and try again."
		}
		s.render(w, http.StatusUnauthorized, "login.html", page{Title: "Sign in", Error: msg})
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/",
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
		MaxAge: int(SessionTTL.Seconds()),
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *server) postLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = s.Auth.Logout(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *server) getPassword(w http.ResponseWriter, r *http.Request, adm store.Admin) {
	s.render(w, http.StatusOK, "change_password.html", page{Title: "Change password", Admin: &adm})
}

func (s *server) postPassword(w http.ResponseWriter, r *http.Request, adm store.Admin) {
	if err := r.ParseForm(); err != nil {
		s.render(w, http.StatusBadRequest, "change_password.html",
			page{Title: "Change password", Admin: &adm, Error: "Malformed form."})
		return
	}
	if err := s.Auth.ChangePassword(r.Context(), adm.ID, r.PostFormValue("password")); err != nil {
		s.render(w, http.StatusBadRequest, "change_password.html",
			page{Title: "Change password", Admin: &adm, Error: err.Error()})
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
