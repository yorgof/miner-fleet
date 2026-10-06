package braiins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"time"
)

// Web uses the local GraphQL API shipped with the S9's 22.08.1 web UI.
// Sessions never leave this process. Variables keep user input out of query text.
type Web struct {
	URL  string
	HTTP *http.Client
}

func NewWeb(url string) *Web {
	jar, _ := cookiejar.New(nil)
	return &Web{url, &http.Client{Jar: jar, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (w *Web) Query(ctx context.Context, q string, vars map[string]any) (map[string]any, error) {
	body, err := json.Marshal(map[string]any{"query": q, "variables": vars})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL+"/graphql", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Braiins web API unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Braiins web API returned HTTP %d", resp.StatusCode)
	}
	var result struct {
		Data   map[string]any `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&result); err != nil {
		return nil, fmt.Errorf("invalid Braiins web response")
	}
	if len(result.Errors) > 0 {
		if strings.Contains(strings.ToLower(result.Errors[0].Message), "unauthorized") {
			return nil, fmt.Errorf("Braiins login required or expired")
		}
		return nil, fmt.Errorf("Braiins API rejected the request")
	}
	if result.Data == nil {
		return nil, fmt.Errorf("Braiins API returned no data")
	}
	if err := resultError(result.Data); err != nil {
		return nil, err
	}
	return result.Data, nil
}
func resultError(v any) error {
	switch x := v.(type) {
	case map[string]any:
		if t, _ := x["__typename"].(string); strings.HasSuffix(t, "Error") {
			return fmt.Errorf("Braiins rejected the operation (%s)", t)
		}
		for _, v := range x {
			if err := resultError(v); err != nil {
				return err
			}
		}
	case []any:
		for _, v := range x {
			if err := resultError(v); err != nil {
				return err
			}
		}
	}
	return nil
}
func (w *Web) Login(ctx context.Context, user, password string) error {
	if user == "" || password == "" {
		return fmt.Errorf("save the miner's web credentials first")
	}
	_, err := w.Query(ctx, `mutation Login($username:String!,$password:String!) {auth {login(username:$username,password:$password){__typename}}}`, map[string]any{"username": user, "password": password})
	return err
}
func (w *Web) Configuration(ctx context.Context) (map[string]any, error) {
	return w.Query(ctx, `query FleetConfig { bos {hostname hwid uptime {since durationS} manager {managementId lastModified} logTargets autoUpgrade faultLight info {mode version {full}} network {__typename ... on StaticIp {address netmask gateway dnsServers}}} bosminer {config {__typename ... on BosminerConfig {hashChainGlobal {asicBoost frequency voltage} tempControl {mode targetTemp hotTemp dangerousTemp} fanControl {speed minFans immersionModeEnabled} autotuning {enabled mode powerTarget hashrateTarget} performanceScaling {enabled powerStep minPowerTarget hashrateStep minHashrateTarget shutdownEnabled shutdownDuration} groups {id name pools {id enabled url user}} hashChains {name enabled frequency voltage} modelDetection {useConfigFallback} management {telemetryEnabled telemetrySubmitPeriodSecs}}}}}`, nil)
}
func (w *Web) Version(ctx context.Context) (map[string]any, error) {
	return w.Query(ctx, `query {bos {hostname info {mode version {full}}} bosminer {info {modelName}}}`, nil)
}

// Mutation permits only named, structured operations; there is no arbitrary command runner.
func (w *Web) Mutation(ctx context.Context, op string, v map[string]any) error {
	var q string
	vars := v
	switch op {
	case "reboot", "start", "stop", "restart":
		root := "bosminer"
		name := op
		result := "BosminerError"
		if op == "reboot" {
			root = "bos"
			result = "BosError"
		}
		q = "mutation {" + root + " {" + name + " {__typename ... on " + result + " {message}}}}"
	case "identify":
		q = `mutation($enable:Boolean!){bos {setFaultLight(enable:$enable){__typename}}}`
	case "hostname":
		q = `mutation($hostname:String!){bos {setHostname(hostname:$hostname){__typename}}}`
	case "password":
		q = `mutation($newPassword:String!){bos {setPassword(newPassword:$newPassword){__typename}}}`
	case "auto_upgrade":
		q = `mutation($enable:Boolean!){bos {autoUpgrade(value:$enable){__typename}}}`
	case "dhcp":
		q = `mutation {bos {network {setDhcp {__typename}}}}`
	case "static_ip":
		q = `mutation($address:String!,$netmask:String!,$gateway:String!,$dnsServers:[String!]!){bos {network {setStaticIp(address:$address,netmask:$netmask,gateway:$gateway,dnsServers:$dnsServers){__typename}}}}`
	case "autotuning", "performance", "cooling":
		typ, name := "AutotuningIn", "updateAutotuning"
		if op == "performance" {
			typ, name = "PerformanceIn", "updatePerformance"
		}
		if op == "cooling" {
			typ, name = "TempAndFansIn", "updateTempAndFans"
		}
		q = "mutation($input:" + typ + "!){bosminer {config {" + name + "(input:$input,apply:true){__typename}}}}"
		vars = map[string]any{"input": v}
	case "pool_add":
		q = `mutation($group:ID!,$url:String!,$user:String!,$password:String,$enabled:Boolean){bosminer {config {group(id:$group){__typename ... on GroupConfigurator {addPool(url:$url,user:$user,password:$password,enabled:$enabled){__typename}}}}}}`
	case "pool_update":
		q = `mutation($group:ID!,$pool:ID!,$url:String,$user:String,$password:String,$enabled:Boolean){bosminer {config {group(id:$group){__typename ... on GroupConfigurator {pool(id:$pool){__typename ... on PoolConfigurator {update(url:$url,user:$user,password:$password,enabled:$enabled){__typename}}}}}}}}`
	case "pool_remove":
		q = `mutation($group:ID!,$pool:ID!){bosminer {config {group(id:$group){__typename ... on GroupConfigurator {removePool(id:$pool){__typename}}}}}}`
	case "pool_move":
		q = `mutation($group:ID!,$pool:ID!,$offset:Int!){bosminer {config {group(id:$group){__typename ... on GroupConfigurator {movePoolByOffset(id:$pool,offset:$offset){__typename}}}}}}`
	case "group_add":
		q = `mutation($name:String!,$quota:Int){bosminer {config {addGroupWithQuota(name:$name,quota:$quota){__typename}}}}`
	case "group_ratio_add":
		q = `mutation($name:String!,$ratio:Float!){bosminer {config {addGroupWithFixedShareRatio(name:$name,ratio:$ratio){__typename}}}}`
	case "group_name":
		q = `mutation($group:ID!,$name:String){bosminer {config {group(id:$group){__typename ... on GroupConfigurator {update(name:$name){__typename}}}}}}`
	case "group_quota":
		q = `mutation($group:ID!,$quota:Int!){bosminer {config {group(id:$group){__typename ... on GroupConfigurator {setQuota(quota:$quota){__typename}}}}}}`
	case "group_ratio":
		q = `mutation($group:ID!,$ratio:Float!){bosminer {config {group(id:$group){__typename ... on GroupConfigurator {setFixedShareRatio(ratio:$ratio){__typename}}}}}}`
	case "pool_clear_password":
		q = `mutation($group:ID!,$pool:ID!){bosminer {config {group(id:$group){__typename ... on GroupConfigurator {pool(id:$pool){__typename ... on PoolConfigurator {removePassword {__typename}}}}}}}}`
	case "group_remove":
		q = `mutation($id:ID!){bosminer {config {removeGroup(id:$id){__typename}}}}`
	case "groups":
		q = `mutation($groups:[Group!]!){bosminer {config {updateGroups(groups:$groups){__typename}}}}`
	default:
		return fmt.Errorf("unsupported Braiins web operation")
	}
	_, err := w.Query(ctx, q, vars)
	return err
}
