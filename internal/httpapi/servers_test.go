package httpapi

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestServerListRenameAndLocalInboundWrites(t *testing.T) {
	f, _, _, _ := newSubscriptionAPI(t)
	if _, err := f.conn.Exec(`INSERT INTO servers(id,name,public_host,status,version,agent_public_key)
		VALUES(2,'Remote','remote.example','offline','0.3.0','reserved-key');
		INSERT INTO inbounds(server_id,tag,protocol,port,options,created_at,updated_at)
		VALUES(2,'vless-reality-443','vless_reality',443,'{}',1,1),
		(2,'remote-panel-port','vless_reality',2053,'{}',1,1);`); err != nil {
		t.Fatal(err)
	}

	base := "https://example.test/api/v1/servers"
	requireStatus(t, request(t, f.h, "GET", base, "", nil, nil), 401)
	requireStatus(t, request(t, f.h, "PUT", base+"/1", `{"name":"Blocked"}`, f.cookie, map[string]string{"Origin": "https://other.test", "Sec-Fetch-Site": "cross-site"}), 403)
	w := f.call(t, "GET", "servers", "", 200)
	var list []serverRecord
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list) != 2 || !list[0].IsLocal || list[0].PublicHost != "2001:db8::8" || list[1].Status != "offline" || list[1].Version != "0.3.0" {
		t.Fatal(list, err)
	}
	if strings.Contains(w.Body.String(), "reserved-key") || strings.Contains(w.Body.String(), "alert_preferences") {
		t.Fatal("server list exposes internal fields")
	}
	for _, body := range []string{`{}`, `{"name":"   "}`, `{"name":"a\tb"}`, `{"name":"new","public_host":"blocked.example"}`, `{"name":"` + strings.Repeat("名", 65) + `"}`} {
		f.call(t, "PUT", "servers/1", body, 400)
	}
	f.call(t, "PUT", "servers/99", `{"name":"Missing"}`, 404)
	calls := f.apply.calls
	w = f.call(t, "PUT", "servers/1", `{"name":"  New name  "}`, 200)
	var renamed serverRecord
	if err := json.Unmarshal(w.Body.Bytes(), &renamed); err != nil || renamed.Name != "New name" || renamed.PublicHost != list[0].PublicHost || f.apply.calls != calls {
		t.Fatal("renaming changed core settings", renamed, err)
	}
	f.call(t, "PUT", "settings", `{"public_host":"changed.example"}`, 200)
	w = f.call(t, "GET", "servers", "", 200)
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || list[0].PublicHost != "changed.example" || list[1].PublicHost != "remote.example" {
		t.Fatal("host sync affected the wrong rows", list, err)
	}

	w = f.call(t, "POST", "inbounds", `{"server_id":2,"protocol":"vless_reality"}`, 422)
	if !strings.Contains(w.Body.String(), "当前版本仅支持管理本机入站") || f.apply.calls != calls {
		t.Fatal("remote create proceeded to Apply")
	}
	w = f.call(t, "POST", "inbounds", `{"server_id":1,"protocol":"vless_reality","port":443,"options":{"server_name":"www.example.com"}}`, 201)
	var local inboundRecord
	if err := json.Unmarshal(w.Body.Bytes(), &local); err != nil || local.ServerID != 1 || local.Port != 443 {
		t.Fatal(local, err)
	}
	path := fmt.Sprintf("inbounds/%d", local.ID)
	f.call(t, "PUT", path, `{"server_id":1,"remark":"local"}`, 200)
	f.call(t, "PUT", path, `{"server_id":2}`, 422)
	f.call(t, "PUT", path, `{"server_id":99}`, 422)
	f.call(t, "PUT", "inbounds/1", `{"remark":"remote edit"}`, 422)
	f.call(t, "DELETE", "inbounds/1", "", 422)
}
