// Copyright (c) 2015 Spinpunch, Inc. All Rights Reserved.
// See License.txt for license information.

package api

import (
	l4g "code.google.com/p/log4go"
	"fmt"
	"github.com/gorilla/mux"
	"github.com/mattermost/platform/model"
	"github.com/mattermost/platform/utils"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func InitTeam(r *mux.Router) {
	l4g.Debug("Initializing team api routes")

	sr := r.PathPrefix("/teams").Subrouter()
	sr.Handle("/create", ApiAppHandler(createTeam)).Methods("POST")
	sr.Handle("/create_from_signup", ApiAppHandler(createTeamFromSignup)).Methods("POST")
	sr.Handle("/signup", ApiAppHandler(signupTeam)).Methods("POST")
	sr.Handle("/find_team_by_url_id", ApiAppHandler(findTeamByURLId)).Methods("POST")
	sr.Handle("/find_teams", ApiAppHandler(findTeams)).Methods("POST")
	sr.Handle("/email_teams", ApiAppHandler(emailTeams)).Methods("POST")
	sr.Handle("/invite_members", ApiUserRequired(inviteMembers)).Methods("POST")
	sr.Handle("/update_name", ApiUserRequired(updateTeamName)).Methods("POST")
	sr.Handle("/update_valet_feature", ApiUserRequired(updateValetFeature)).Methods("POST")
	sr.Handle("/me", ApiUserRequired(getMyTeam)).Methods("GET")
}

func signupTeam(c *Context, w http.ResponseWriter, r *http.Request) {

	m := model.MapFromJson(r.Body)
	email := strings.ToLower(strings.TrimSpace(m["email"]))
	name := strings.TrimSpace(m["name"])

	if len(email) == 0 {
		c.SetInvalidParam("signupTeam", "email")
		return
	}

	if len(name) == 0 {
		c.SetInvalidParam("signupTeam", "name")
		return
	}

	subjectPage := NewServerTemplatePage("signup_team_subject", c.SiteUrl)
	bodyPage := NewServerTemplatePage("signup_team_body", c.SiteUrl)
	bodyPage.Props["TourUrl"] = utils.Cfg.TeamSettings.TourLink

	props := make(map[string]string)
	props["email"] = email
	props["name"] = name
	props["time"] = fmt.Sprintf("%v", model.GetMillis())

	data := model.MapToJson(props)
	hash := model.HashPassword(fmt.Sprintf("%v:%v", data, utils.Cfg.ServiceSettings.InviteSalt))

	bodyPage.Props["Link"] = fmt.Sprintf("%s/signup_team_complete/?d=%s&h=%s", c.SiteUrl, url.QueryEscape(data), url.QueryEscape(hash))

	if err := utils.SendMail(email, subjectPage.Render(), bodyPage.Render()); err != nil {
		c.Err = err
		return
	}

	if utils.Cfg.ServiceSettings.Mode == utils.MODE_DEV {
		m["follow_link"] = bodyPage.Props["Link"]
	}

	w.Header().Set("Access-Control-Allow-Origin", " *")
	w.Write([]byte(model.MapToJson(m)))
}

func createTeamFromSignup(c *Context, w http.ResponseWriter, r *http.Request) {

	teamSignup := model.TeamSignupFromJson(r.Body)

	if teamSignup == nil {
		c.SetInvalidParam("createTeam", "teamSignup")
		return
	}

	props := model.MapFromJson(strings.NewReader(teamSignup.Data))
	teamSignup.Team.Email = props["email"]
	teamSignup.User.Email = props["email"]

	teamSignup.Team.PreSave()

	if err := teamSignup.Team.IsValid(); err != nil {
		c.Err = err
		return
	}
	teamSignup.Team.Id = ""

	password := teamSignup.User.Password
	teamSignup.User.PreSave()
	teamSignup.User.TeamId = model.NewId()
	if err := teamSignup.User.IsValid(); err != nil {
		c.Err = err
		return
	}
	teamSignup.User.Id = ""
	teamSignup.User.TeamId = ""
	teamSignup.User.Password = password

	if !model.ComparePassword(teamSignup.Hash, fmt.Sprintf("%v:%v", teamSignup.Data, utils.Cfg.ServiceSettings.InviteSalt)) {
		c.Err = model.NewAppError("createTeamFromSignup", "The signup link does not appear to be valid", "")
		return
	}

	t, err := strconv.ParseInt(props["time"], 10, 64)
	if err != nil || model.GetMillis()-t > 1000*60*60 { // one hour
		c.Err = model.NewAppError("createTeamFromSignup", "The signup link has expired", "")
		return
	}

	found := FindTeamByURLId(c, teamSignup.Team.URLId, "true")
	if c.Err != nil {
		return
	}

	if found {
		c.Err = model.NewAppError("createTeamFromSignup", "This URL is unavailable. Please try another.", "d="+teamSignup.Team.URLId)
		return
	}

	teamSignup.Team.AllowValet = utils.Cfg.TeamSettings.AllowValetDefault

	if result := <-Srv.Store.Team().Save(&teamSignup.Team); result.Err != nil {
		c.Err = result.Err
		return
	} else {
		rteam := result.Data.(*model.Team)

		if _, err := CreateDefaultChannels(c, rteam.Id); err != nil {
			c.Err = nil
			return
		}

		teamSignup.User.TeamId = rteam.Id
		teamSignup.User.EmailVerified = true

		ruser := CreateUser(c, rteam, &teamSignup.User)
		if c.Err != nil {
			return
		}

		if teamSignup.Team.AllowValet {
			CreateValet(c, rteam)
			if c.Err != nil {
				return
			}
		}

		InviteMembers(c, rteam, ruser, teamSignup.Invites)

		teamSignup.Team = *rteam
		teamSignup.User = *ruser

		w.Write([]byte(teamSignup.ToJson()))
	}
}

func createTeam(c *Context, w http.ResponseWriter, r *http.Request) {

	team := model.TeamFromJson(r.Body)

	if team == nil {
		c.SetInvalidParam("createTeam", "team")
		return
	}

	if utils.Cfg.ServiceSettings.Mode != utils.MODE_DEV {
		c.Err = model.NewAppError("createTeam", "The mode does not allow network creation without a valid invite", "")
		return
	}

	if result := <-Srv.Store.Team().Save(team); result.Err != nil {
		c.Err = result.Err
		return
	} else {
		rteam := result.Data.(*model.Team)

		if _, err := CreateDefaultChannels(c, rteam.Id); err != nil {
			c.Err = nil
			return
		}

		if rteam.AllowValet {
			CreateValet(c, rteam)
			if c.Err != nil {
				return
			}
		}

		w.Write([]byte(rteam.ToJson()))
	}
}

func findTeamByURLId(c *Context, w http.ResponseWriter, r *http.Request) {

	m := model.MapFromJson(r.Body)

	urlId := strings.ToLower(strings.TrimSpace(m["urlId"]))
	all := strings.ToLower(strings.TrimSpace(m["all"]))

	found := FindTeamByURLId(c, urlId, all)

	w.Header().Set(model.HEADER_TEAM_URL, c.TeamUrl+"/"+urlId)

	if c.Err != nil {
		return
	}

	if found {
		w.Write([]byte("true"))
	} else {
		w.Write([]byte("false"))
	}
}

func FindTeamByURLId(c *Context, urlId string, all string) bool {

	if urlId == "" || len(urlId) > 64 {
		c.SetInvalidParam("findTeamByURLId", "domain")
		return false
	}

	if model.IsReservedURLId(urlId) {
		c.Err = model.NewAppError("findTeamByURLId", "This URL is unavailable. Please try another.", "urlid="+urlId)
		return false
	}

	if result := <-Srv.Store.Team().GetByURLId(urlId); result.Err != nil {
		return false
	} else {
		return true
	}

	return false
}

func findTeams(c *Context, w http.ResponseWriter, r *http.Request) {

	m := model.MapFromJson(r.Body)

	email := strings.ToLower(strings.TrimSpace(m["email"]))

	if email == "" {
		c.SetInvalidParam("findTeam", "email")
		return
	}

	if result := <-Srv.Store.Team().GetTeamsForEmail(email); result.Err != nil {
		c.Err = result.Err
		return
	} else {
		teams := result.Data.([]*model.Team)

		s := make([]string, 0, len(teams))

		for _, v := range teams {
			s = append(s, v.URLId)
		}

		w.Write([]byte(model.ArrayToJson(s)))
	}
}

func emailTeams(c *Context, w http.ResponseWriter, r *http.Request) {

	m := model.MapFromJson(r.Body)

	email := strings.ToLower(strings.TrimSpace(m["email"]))

	if email == "" {
		c.SetInvalidParam("findTeam", "email")
		return
	}

	subjectPage := NewServerTemplatePage("find_teams_subject", c.TeamUrl)
	bodyPage := NewServerTemplatePage("find_teams_body", c.TeamUrl)

	if err := utils.SendMail(email, subjectPage.Render(), bodyPage.Render()); err != nil {
		l4g.Error("An error occured while sending an email in emailTeams err=%v", err)
	}

	w.Write([]byte(model.MapToJson(m)))
}

func inviteMembers(c *Context, w http.ResponseWriter, r *http.Request) {
	invites := model.InvitesFromJson(r.Body)
	if len(invites.Invites) == 0 {
		c.Err = model.NewAppError("Team.InviteMembers", "No one to invite.", "")
		c.Err.StatusCode = http.StatusBadRequest
		return
	}

	tchan := Srv.Store.Team().Get(c.Session.TeamId)
	uchan := Srv.Store.User().Get(c.Session.UserId)

	var team *model.Team
	if result := <-tchan; result.Err != nil {
		c.Err = result.Err
		return
	} else {
		team = result.Data.(*model.Team)
	}

	var user *model.User
	if result := <-uchan; result.Err != nil {
		c.Err = result.Err
		return
	} else {
		user = result.Data.(*model.User)
	}

	var invNum int64 = 0
	for i, invite := range invites.Invites {
		if result := <-Srv.Store.User().GetByEmail(c.Session.TeamId, invite["email"]); result.Err == nil || result.Err.Message != "We couldn't find the existing account" {
			invNum = int64(i)
			c.Err = model.NewAppError("invite_members", "This person is already on your team", strconv.FormatInt(invNum, 10))
			return
		}
	}

	ia := make([]string, len(invites.Invites))
	for _, invite := range invites.Invites {
		ia = append(ia, invite["email"])
	}

	InviteMembers(c, team, user, ia)

	w.Write([]byte(invites.ToJson()))
}

func InviteMembers(c *Context, team *model.Team, user *model.User, invites []string) {
	for _, invite := range invites {
		if len(invite) > 0 {
			teamUrl := ""
			teamUrl = c.TeamUrl

			sender := ""
			if len(strings.TrimSpace(user.FullName)) == 0 {
				sender = user.Username
			} else {
				sender = user.FullName
			}

			senderRole := ""
			if strings.Contains(user.Roles, model.ROLE_ADMIN) || strings.Contains(user.Roles, model.ROLE_SYSTEM_ADMIN) {
				senderRole = "administrator"
			} else {
				senderRole = "member"
			}

			subjectPage := NewServerTemplatePage("invite_subject", teamUrl)
			subjectPage.Props["SenderName"] = sender
			subjectPage.Props["TeamName"] = team.Name
			bodyPage := NewServerTemplatePage("invite_body", teamUrl)
			bodyPage.Props["TeamName"] = team.Name
			bodyPage.Props["SenderName"] = sender
			bodyPage.Props["SenderStatus"] = senderRole

			bodyPage.Props["Email"] = invite

			props := make(map[string]string)
			props["email"] = invite
			props["id"] = team.Id
			props["name"] = team.Name
			props["urlId"] = team.URLId
			props["time"] = fmt.Sprintf("%v", model.GetMillis())
			data := model.MapToJson(props)
			hash := model.HashPassword(fmt.Sprintf("%v:%v", data, utils.Cfg.ServiceSettings.InviteSalt))
			bodyPage.Props["Link"] = fmt.Sprintf("%s/signup_user_complete/?d=%s&h=%s", teamUrl, url.QueryEscape(data), url.QueryEscape(hash))

			if utils.Cfg.ServiceSettings.Mode == utils.MODE_DEV {
				l4g.Info("sending invitation to %v %v", invite, bodyPage.Props["Link"])
			}

			if err := utils.SendMail(invite, subjectPage.Render(), bodyPage.Render()); err != nil {
				l4g.Error("Failed to send invite email successfully err=%v", err)
			}
		}
	}
}

func updateTeamName(c *Context, w http.ResponseWriter, r *http.Request) {

	props := model.MapFromJson(r.Body)

	new_name := props["new_name"]
	if len(new_name) == 0 {
		c.SetInvalidParam("updateTeamName", "new_name")
		return
	}

	teamId := props["team_id"]
	if len(teamId) > 0 && len(teamId) != 26 {
		c.SetInvalidParam("updateTeamName", "team_id")
		return
	} else if len(teamId) == 0 {
		teamId = c.Session.TeamId
	}

	if !c.HasPermissionsToTeam(teamId, "updateTeamName") {
		return
	}

	if !strings.Contains(c.Session.Roles, model.ROLE_ADMIN) {
		c.Err = model.NewAppError("updateTeamName", "You do not have the appropriate permissions", "userId="+c.Session.UserId)
		c.Err.StatusCode = http.StatusForbidden
		return
	}

	if result := <-Srv.Store.Team().UpdateName(new_name, c.Session.TeamId); result.Err != nil {
		c.Err = result.Err
		return
	}

	w.Write([]byte(model.MapToJson(props)))
}

func updateValetFeature(c *Context, w http.ResponseWriter, r *http.Request) {

	props := model.MapFromJson(r.Body)

	allowValetStr := props["allow_valet"]
	if len(allowValetStr) == 0 {
		c.SetInvalidParam("updateValetFeature", "allow_valet")
		return
	}

	allowValet := allowValetStr == "true"

	teamId := props["team_id"]
	if len(teamId) > 0 && len(teamId) != 26 {
		c.SetInvalidParam("updateValetFeature", "team_id")
		return
	} else if len(teamId) == 0 {
		teamId = c.Session.TeamId
	}

	tchan := Srv.Store.Team().Get(teamId)

	if !c.HasPermissionsToTeam(teamId, "updateValetFeature") {
		return
	}

	if !strings.Contains(c.Session.Roles, model.ROLE_ADMIN) {
		c.Err = model.NewAppError("updateValetFeature", "You do not have the appropriate permissions", "userId="+c.Session.UserId)
		c.Err.StatusCode = http.StatusForbidden
		return
	}

	var team *model.Team
	if tResult := <-tchan; tResult.Err != nil {
		c.Err = tResult.Err
		return
	} else {
		team = tResult.Data.(*model.Team)
	}

	team.AllowValet = allowValet

	if result := <-Srv.Store.Team().Update(team); result.Err != nil {
		c.Err = result.Err
		return
	}

	w.Write([]byte(model.MapToJson(props)))
}

func getMyTeam(c *Context, w http.ResponseWriter, r *http.Request) {

	if len(c.Session.TeamId) == 0 {
		return
	}

	if result := <-Srv.Store.Team().Get(c.Session.TeamId); result.Err != nil {
		c.Err = result.Err
		return
	} else if HandleEtag(result.Data.(*model.Team).Etag(), w, r) {
		return
	} else {
		w.Header().Set(model.HEADER_ETAG_SERVER, result.Data.(*model.Team).Etag())
		w.Header().Set("Expires", "-1")
		w.Write([]byte(result.Data.(*model.Team).ToJson()))
		return
	}
}
