package mcgo

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"strconv"
	"time"
)

func (account *MCaccount) AuthenticatedReq(method string, url string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	if account.Bearer == "" {
		return nil, errors.New("account is not authenticated")
	}
	req.Header.Add("Authorization", "Bearer "+account.Bearer)
	req.Header.Set("Content-Type", "application/json")

	return req, nil
}

type AccType string

const (
	Ms   AccType = "ms"
	Mj   AccType = "mj"
	MsPr AccType = "mspr"
)

// TODO: Use RequestError for status-code-related errors
type RequestError struct {
	StatusCode int
	Err        error
}

func (r *RequestError) Error() string {
	return r.Err.Error()
}

// represents a minecraft account
type MCaccount struct {
	Email             string
	Password          string
	SecurityQuestions []SqAnswer
	SecurityAnswers   []string
	Bearer            string
	UUID              string
	Username          string
	Type              AccType
	Authenticated     bool
}

type authenticateReqResp struct {
	User struct {
		Properties []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"properties"`
		Username string `json:"username"`
		ID       string `json:"id"`
	} `json:"user"`
	Accesstoken string `json:"accessToken"`
	Clienttoken string `json:"clientToken"`
}

func (account *MCaccount) authenticate() error {
	payload := fmt.Sprintf(`{
    "agent": {                              
        "name": "Minecraft",                
        "version": 1                        
    },
    "username": "%s",      
    "password": "%s",
	"requestUser": true
}`, account.Email, account.Password)

	u := bytes.NewReader([]byte(payload))
	request, err := http.NewRequest("POST", "https://authserver.mojang.com/authenticate", u)
	request.Header.Set("Content-Type", "application/json")

	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(request)

	if err != nil {
		return err
	}

	defer resp.Body.Close()

	if resp.StatusCode < 300 {
		var AccountInfo authenticateReqResp
		b, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		err = json.Unmarshal(b, &AccountInfo)
		if err != nil {
			return err
		}

		account.Bearer = AccountInfo.Accesstoken
		account.Username = AccountInfo.User.Username
		account.UUID = AccountInfo.User.ID
		return nil

	} else if resp.StatusCode == 403 {
		return errors.New("invalid email or password")
	}
	return errors.New("reached end of authenticate function! Shouldn't be possible. most likely 'failed to auth' status code changed")
}

type SqAnswer struct {
	Answer struct {
		ID int `json:"id"`
	} `json:"answer"`
	Question struct {
		ID       int    `json:"id"`
		Question string `json:"question"`
	} `json:"question"`
}

func (account *MCaccount) loadSecurityQuestions() error {
	req, err := account.AuthenticatedReq("GET", "https://api.mojang.com/user/security/challenges", nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("got status %v when requesting security questions", resp.Status)
	}

	defer resp.Body.Close()

	var sqAnswers []SqAnswer

	respBytes, err := ioutil.ReadAll(resp.Body)

	if err != nil {
		return err
	}

	err = json.Unmarshal(respBytes, &sqAnswers)
	if err != nil {
		return err
	}

	account.SecurityQuestions = sqAnswers

	return nil
}

type accInfoResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// load account information (username, uuid) into accounts attributes, if not already there. When using Mojang authentication it is not necessary to load this info, as it will be automatically loaded.
func (account *MCaccount) LoadAccountInfo() error {
	req, err := account.AuthenticatedReq("GET", "https://api.minecraftservices.com/minecraft/profile", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)

	if err != nil {
		return err
	}

	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return &RequestError{
			StatusCode: resp.StatusCode,
			Err:        errors.New("account does not own minecraft"),
		}
	}

	respBytes, err := ioutil.ReadAll(resp.Body)

	if err != nil {
		return err
	}

	var respJson accInfoResponse

	json.Unmarshal(respBytes, &respJson)

	account.Username = respJson.Name
	account.UUID = respJson.ID

	return nil
}

func (account *MCaccount) needToAnswer() (bool, error) {
	req, err := account.AuthenticatedReq("GET", "https://api.mojang.com/user/security/location", nil)
	if err != nil {
		return false, err
	}

	resp, err := http.DefaultClient.Do(req)

	if err != nil {
		return true, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 204 {
		return false, nil
	}
	if resp.StatusCode == 403 {
		return true, nil
	}
	return true, fmt.Errorf("status of %v in needToAnswer not expected", resp.Status)
}

type submitPostJson struct {
	ID     int    `json:"id"`
	Answer string `json:"answer"`
}

func (account *MCaccount) submitAnswers() error {
	if len(account.SecurityAnswers) != 3 {
		return errors.New("not enough security question answers provided")
	}
	if len(account.SecurityQuestions) != 3 {
		return errors.New("security questions not properly loaded")
	}
	var jsonContent []submitPostJson
	for i, sq := range account.SecurityQuestions {
		jsonContent = append(jsonContent, submitPostJson{ID: sq.Answer.ID, Answer: account.SecurityAnswers[i]})
	}
	jsonStr, err := json.Marshal(jsonContent)
	if err != nil {
		return err
	}
	req, err := account.AuthenticatedReq("POST", "https://api.mojang.com/user/security/location", bytes.NewBuffer([]byte(jsonStr)))
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)

	if err != nil {
		return err
	}

	if resp.StatusCode == 204 {
		return nil
	}

	defer resp.Body.Close()

	if resp.StatusCode == 403 {
		return errors.New("at least one security question answer was incorrect")
	}
	return fmt.Errorf("got status %v on post request for sqs", resp.Status)
}

// Runs all steps necessary to have a fully authenticated mojang account. It will submit email & pass and securitty questions (if necessary).
func (account *MCaccount) MojangAuthenticate() error {
	err := account.authenticate()
	if err != nil {
		return err
	}
	err = account.loadSecurityQuestions()

	if err != nil {
		return err
	}

	if len(account.SecurityQuestions) == 0 {
		account.Authenticated = true
		return nil
	}

	answerNeeded, err := account.needToAnswer()
	if err != nil {
		return err
	}

	if !answerNeeded {
		account.Authenticated = true
		return nil
	}

	err = account.submitAnswers()
	if err != nil {
		return err
	}

	account.Authenticated = true
	return nil
}

type HasGcAppliedResp struct {
	Path             string `json:"path"`
	ErrorType        string `json:"errorType"`
	Error            string `json:"error"`
	ErrorMessage     string `json:"errorMessage"`
	DeveloperMessage string `json:"developerMessage"`
	Details          struct {
		Status string `json:"status"`
	} `json:"details"`
}

func (account *MCaccount) HasGcApplied() (bool, error) {
	bodyStr := `{"profileName": "test"}`
	req, err := account.AuthenticatedReq("POST", "https://api.minecraftservices.com/minecraft/profile", bytes.NewReader([]byte(bodyStr)))
	if err != nil {
		return false, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}

	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		return false, &RequestError{
			StatusCode: resp.StatusCode,
			Err:        errors.New("successfully created profile with name test.. unintended behavior, function is meant to check if gc is applied"),
		}
	} else if resp.StatusCode == 401 {
		return false, &RequestError{
			StatusCode: resp.StatusCode,
			Err:        errors.New("received unauthorized response"),
		}
	} else if resp.StatusCode == 400 {
		var respError HasGcAppliedResp
		bodyBytes, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return false, err
		}

		err = json.Unmarshal(bodyBytes, &respError)
		if err != nil {
			return false, err
		}

		var hasGc bool

		switch respError.Details.Status {
		case "ALREADY_REGISTERED", "NOT_ENTITLED":
			{
				hasGc = false
			}
		case "DUPLICATE", "NOT_ALLOWED":
			{
				hasGc = true
			}
		default:
			{
				hasGc = false
			}
		}

		return hasGc, nil

	}

	return false, fmt.Errorf("got status %v, expected 200, 401, or 400", resp.StatusCode)

}

// Holds name change information for an account, the time the current account was created, it's name was most recently changed, and if it can currently change its name.
type nameChangeInfoResponse struct {
	Changedat         time.Time `json:"changedAt"`
	Createdat         time.Time `json:"createdAt"`
	Namechangeallowed bool      `json:"nameChangeAllowed"`
}

// grab information on the availability of name change for this account
func (account *MCaccount) NameChangeInfo() (nameChangeInfoResponse, error) {
	client := &http.Client{}
	req, err := account.AuthenticatedReq("GET", "https://api.minecraftservices.com/minecraft/profile/namechange", nil)

	if err != nil {
		return nameChangeInfoResponse{}, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nameChangeInfoResponse{}, err
	}
	defer resp.Body.Close()

	respBody, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nameChangeInfoResponse{}, err
	}

	if resp.StatusCode >= 400 {
		return nameChangeInfoResponse{
				Changedat:         time.Time{},
				Createdat:         time.Time{},
				Namechangeallowed: false,
			}, &RequestError{
				StatusCode: resp.StatusCode,
				Err:        errors.New("failed to grab name change info"),
			}
	}

	var parsedNameChangeInfo nameChangeInfoResponse

	err = json.Unmarshal(respBody, &parsedNameChangeInfo)

	if err != nil {
		return nameChangeInfoResponse{}, err
	}

	return parsedNameChangeInfo, nil
}

type NameChangeReturn struct {
	Account     MCaccount
	Username    string
	ChangedName bool
	StatusCode  int
	SendTime    time.Time
	ReceiveTime time.Time
}

func (account *MCaccount) ChangeName(username string, changeTime time.Time, createProfile bool) (NameChangeReturn, error) {

	var payload string
	if createProfile {
		data := fmt.Sprintf(`{"profileName": "%s"}`, username)
		payload = fmt.Sprintf(
			"POST /minecraft/profile HTTP/1.1\r\n"+
				"Host: api.minecraftservices.com\r\n"+
				"Authorization: Bearer %s\r\n"+
				"Content-Type: application/json\r\n"+
				"Content-Length: %d\r\n"+
				"\r\n"+
				"%s",
			account.Bearer,
			len(data),
			data,
		)
		// credit to peet for that ^
		// and credit to tenscape for teaching me how HTTP works lol
	} else {
		payload = fmt.Sprintf("PUT /minecraft/profile/name/%s HTTP/1.1\r\nHost: api.minecraftservices.com\r\nAuthorization: Bearer %s\r\n\r\n", username, account.Bearer)
		// and that
	}

	recvd := make([]byte, 4096)

	time.Sleep(time.Until(changeTime) - time.Second*20)

	conn, err := tls.Dial("tcp", "api.minecraftservices.com"+":443", nil)
	conn.Write([]byte(payload[:len(payload)-2]))
	if err != nil {
		return NameChangeReturn{
			Account:     MCaccount{},
			Username:    username,
			ChangedName: false,
			StatusCode:  0,
			SendTime:    time.Time{},
			ReceiveTime: time.Time{},
		}, err
	}

	time.Sleep(time.Until(changeTime))

	conn.Write([]byte(payload[len(payload)-2:]))
	sendTime := time.Now()

	conn.Read(recvd)
	recvTime := time.Now()
	conn.Close()
	status, err := strconv.Atoi(string(recvd[9:12]))

	if err != nil {
		return NameChangeReturn{
			Account:     MCaccount{},
			Username:    username,
			ChangedName: false,
			StatusCode:  0,
			SendTime:    sendTime,
			ReceiveTime: time.Time{},
		}, err
	}

	toRet := NameChangeReturn{
		Account:     *account,
		Username:    username,
		ChangedName: status < 300,
		StatusCode:  status,
		SendTime:    sendTime,
		ReceiveTime: recvTime,
	}
	return toRet, nil
}
func (account *MCaccount) ChangeName1(username string, changeTime time.Time, createProfile bool) (NameChangeReturn, error) {

	var payload string
	if createProfile {
		data := fmt.Sprintf(`{"profileName": "%s"}`, username)
		payload = fmt.Sprintf(
			"POST /minecraft/profile HTTP/1.1\r\n"+
				"Host: api.minecraftservices.com\r\n"+
				"Authorization: Bearer %s\r\n"+
				"Content-Type: application/json\r\n"+
				"Content-Length: %d\r\n"+
				"\r\n"+
				"%s",
			account.Bearer,
			len(data),
			data,
		)
		// credit to peet for that ^
		// and credit to tenscape for teaching me how HTTP works lol
	} else {
		payload = fmt.Sprintf("POST /minecraft/profile HTTP/1.1\r\nHost: api.minecraftservices.com\r\nAuthorization: Bearer %s\r\n\r\n", account.Bearer)
		// and that
	}

	recvd := make([]byte, 4096)

	time.Sleep(time.Until(changeTime) - time.Second*20)

	conn, err := tls.Dial("tcp", "api.minecraftservices.com"+":443", nil)
	conn.Write([]byte(payload[:len(payload)-2]))
	if err != nil {
		return NameChangeReturn{
			Account:     MCaccount{},
			Username:    username,
			ChangedName: false,
			StatusCode:  0,
			SendTime:    time.Time{},
			ReceiveTime: time.Time{},
		}, err
	}

	time.Sleep(time.Until(changeTime))

	conn.Write([]byte(payload[len(payload)-2:]))
	sendTime := time.Now()

	conn.Read(recvd)
	recvTime := time.Now()
	conn.Close()
	status, err := strconv.Atoi(string(recvd[9:12]))

	if err != nil {
		return NameChangeReturn{
			Account:     MCaccount{},
			Username:    username,
			ChangedName: false,
			StatusCode:  0,
			SendTime:    sendTime,
			ReceiveTime: time.Time{},
		}, err
	}

	toRet := NameChangeReturn{
		Account:     *account,
		Username:    username,
		ChangedName: status < 300,
		StatusCode:  status,
		SendTime:    sendTime,
		ReceiveTime: recvTime,
	}
	return toRet, nil
}
