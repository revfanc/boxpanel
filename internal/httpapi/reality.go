package httpapi

import (
	"errors"
	"net/http"

	"github.com/revfanc/boxpanel/internal/reality"
	"github.com/revfanc/boxpanel/internal/sysinfo"
)

func (a *api) realityRecommend(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"targets": reality.Recommend(r.Context(), a.opts.RealityProbe)})
}
func (a *api) realityCheck(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Server string `json:"server"`
		Port   *int   `json:"port"`
	}
	if !decodeResource(w, r, &input) {
		return
	}
	port := 443
	if input.Port != nil {
		port = *input.Port
	}
	if !reality.ValidServer(input.Server) || port < 1 || port > 65535 {
		a.resourceError(w, invalid("server/port", "请填写公网域名或公网 IP，以及 1–65535 之间的端口（默认 443）；不要填写 URL。"))
		return
	}
	writeJSON(w, 200, a.opts.RealityProbe(r.Context(), input.Server, port))
}
func (a *api) inboundSuggest(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Protocol string `json:"protocol"`
		ServerID *int64 `json:"server_id"`
	}
	if !decodeResource(w, r, &input) {
		return
	}
	conn, err := a.readDB()
	if err != nil {
		a.resourceError(w, err)
		return
	}
	server, err := lookupInboundServer(r.Context(), conn, input.ServerID)
	if err == nil {
		err = checkInboundServer(server)
	}
	if err != nil {
		a.resourceError(w, err)
		return
	}
	options, err := a.prepareInbound(r.Context(), input.Protocol, nil, server.local)
	hint := ""
	var targetErr *resourceError
	if errors.As(err, &targetErr) && targetErr.code == "reality_target_unavailable" {
		hint = targetErr.hint
		options, err = defaultInboundOptions(input.Protocol, "", true)
	}
	if err != nil {
		a.resourceError(w, err)
		return
	}
	var sockets map[sysinfo.Socket]bool
	if server.local {
		sockets, err = a.opts.ListeningPorts(r.Context())
		if err != nil {
			a.portInspectionError(w)
			return
		}
	}
	reservations, err := a.inboundReservations(r.Context(), conn, sockets, server)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	port, err := reservations.suggest(input.Protocol)
	if err != nil {
		a.resourceError(w, err)
		return
	}
	in, err := publicInbound(inboundRecord{Protocol: input.Protocol, Options: options})
	if err != nil {
		a.resourceError(w, err)
		return
	}
	// Suggest has no server-side state. Do not expose a private key or a public
	// key that would be mismatched when creation generates fresh credentials.
	fields := optionFields(in.Options)
	delete(fields, "public_key")
	result := map[string]any{"protocol": input.Protocol, "port": port, "options": fields}
	if hint != "" {
		result["hint"] = hint
	}
	writeJSON(w, 200, result)
}
