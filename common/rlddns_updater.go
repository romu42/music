package music

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/miekg/dns"
)

type RLDdnsUpdater struct {
	FetchCh  chan SignerOp
	UpdateCh chan SignerOp
	Api      Api
}

func init() {
	Updaters["rlddns"] = &RLDdnsUpdater{}
}

func (u *RLDdnsUpdater) SetChannels(fetch, update chan SignerOp) {
	u.FetchCh = fetch
	u.UpdateCh = update
}

// DDNS has no API
func (u *RLDdnsUpdater) SetApi(api Api) {
	// no-op
}

func (u *RLDdnsUpdater) GetApi() Api {
	// no-op
	return Api{}
}

func (u *RLDdnsUpdater) Update(signer *Signer, zone, fqdn string,
	inserts, removes *[][]dns.RR) error {
	op := SignerOp{}
	u.UpdateCh <- op
	time.Sleep(1 * time.Second)
	resp := <-op.Response
	return resp.Error
}

// Note: for DDNS we do not implement any real rate-limiting right now (other than the
// voluntary restriction to the limits set in the config). But we keep the same interface with
// rate-limited (bool), hold in seconds (int), error (error) as for deSEC and other APIs.
//
func RLDdnsUpdate(udop SignerOp) (bool, int, error) {
	signer := udop.Signer
	owner := udop.Owner
	inserts := udop.Inserts
	removes := udop.Removes

	log.Printf("RLDDNS Updater: signer: %s, fqdn: %s inserts: %v removes: %v\n",
		signer.Name, owner, inserts, removes)
	inserts_len := 0
	removes_len := 0
	if inserts != nil {
		for _, insert := range *inserts {
			inserts_len += len(insert)
		}
	}
	if removes != nil {
		for _, remove := range *removes {
			removes_len += len(remove)
		}
	}

	var err error
	if inserts_len == 0 && removes_len == 0 {
		err = fmt.Errorf("Inserts and removes empty, nothing to do")
	} else if signer.Address == "" {
		err = fmt.Errorf("No ip|host for signer %s", signer.Name)
	} else if signer.Auth == "" {
		err = fmt.Errorf("No TSIG for signer %s", signer.Name)
	}
	tsig := strings.SplitN(signer.Auth, ":", 2) // is this safe if signer.Auth == ""?
	if len(tsig) != 2 {
		err = fmt.Errorf("Incorrect TSIG for signer %s", signer.Name)
	}
	if err != nil {
		udop.Response <- SignerOpResult{Error: err}
		return false, 0, nil // return to ddnsmgr: no rate-limiting, no hold
	}

	m := new(dns.Msg)
	m.SetUpdate(owner)
	if inserts != nil {
		for _, insert := range *inserts {
			m.Insert(insert)
		}
	}
	if removes != nil {
		for _, remove := range *removes {
			m.Remove(remove)
		}
	}
	m.SetTsig(tsig[0]+".", dns.HmacSHA256, 300, time.Now().Unix())

	// c := new(dns.Client)
	c := dns.Client{Net: "tcp"}
	c.TsigSecret = map[string]string{tsig[0] + ".": tsig[1]}
	in, _, err := c.Exchange(m, signer.Address+":53") // TODO: add DnsAddress or solve this in a better way
	if err != nil {
		udop.Response <- SignerOpResult{Error: err}
		return false, 0, nil // return to ddnsmgr: no rate-limiting, no hold
	}
	if in.MsgHdr.Rcode != dns.RcodeSuccess {
		udop.Response <- SignerOpResult{
			Error: fmt.Errorf("Update failed, RCODE = %s", dns.RcodeToString[in.MsgHdr.Rcode]),
		}
		return false, 0, nil // return to ddnsmgr: no rate-limiting, no hold
	}
	udop.Response <- SignerOpResult{Error: nil}
	return false, 0, nil // return to ddnsmgr: no rate-limiting, no hold
}

func (u *RLDdnsUpdater) RemoveRRset(signer *Signer, zone, fqdn string, rrsets [][]dns.RR) error {
	rrsets_len := 0
	for _, rrset := range rrsets {
		rrsets_len += len(rrset)
	}
	if rrsets_len == 0 {
		return fmt.Errorf("rrset(s) is empty, nothing to do")
	}

	if signer.Address == "" {
		return fmt.Errorf("No ip|host for signer %s", signer.Name)
	}
	if signer.Auth == "" {
		return fmt.Errorf("No TSIG for signer %s", signer.Name)
	}
	tsig := strings.SplitN(signer.Auth, ":", 2)
	if len(tsig) != 2 {
		return fmt.Errorf("Incorrect TSIG for signer %s", signer.Name)
	}

	m := new(dns.Msg)
	m.SetUpdate(fqdn)
	for _, rrset := range rrsets {
		m.RemoveRRset(rrset)
	}
	m.SetTsig(tsig[0]+".", dns.HmacSHA256, 300, time.Now().Unix())

	c := new(dns.Client)
	c.TsigSecret = map[string]string{tsig[0] + ".": tsig[1]}
	in, _, err := c.Exchange(m, signer.Address+":53") // TODO: add DnsAddress or solve this in a better way
	if err != nil {
		return err
	}
	if in.MsgHdr.Rcode != dns.RcodeSuccess {
		return fmt.Errorf("Update failed, RCODE = %s", dns.RcodeToString[in.MsgHdr.Rcode])
	}

	return nil
}

func (u *RLDdnsUpdater) FetchRRset(s *Signer, zone, owner string,
	rrtype uint16) (error, []dns.RR) {

	// fmt.Printf("rlddns.FetchRRset: received query for '%s %s'\n", owner, dns.TypeToString[rrtype])

	op := SignerOp{
		Signer:   s,
		Zone:     zone,
		Owner:    owner,
		RRtype:   rrtype,
		Response: make(chan SignerOpResult, 2),
	}
	u.FetchCh <- op
	time.Sleep(1 * time.Second)
	resp := <-op.Response
	// fmt.Printf("rlddns.FetchRRset: response received, returning\n")
	return resp.Error, resp.RRs
}

func RLDdnsFetchRRset(fdop SignerOp) (bool, int, error) {
	signer := fdop.Signer
	owner := fdop.Owner
	rrtype := fdop.RRtype
	var err error

	// fmt.Printf("RLDdnsFetchRRset: received query for '%s %s'\n", owner, dns.TypeToString[rrtype])
	if signer.Address == "" {
		err = fmt.Errorf("No ip|host for signer %s", signer.Name)
	}
	if signer.Auth == "" {
		err = fmt.Errorf("No TSIG for signer %s", signer.Name)
	}
	tsig := strings.SplitN(signer.Auth, ":", 2)
	if len(tsig) != 2 {
		err = fmt.Errorf("Incorrect TSIG for signer %s", signer.Name)
	}
	if err != nil {
		fmt.Printf("RLDdnsFetchRRset: Pre-req error: %v. Returning response chan + call stack\n", err)
		fdop.Response <- SignerOpResult{Error: err}
		// fmt.Printf("RLDdnsFetchRRset: post response chan after prereq error\n", err)
		return false, 0, nil
	}

	m := new(dns.Msg)
	m.SetQuestion(owner, rrtype)
	// m.SetEdns0(4096, true)
	m.SetTsig(tsig[0]+".", dns.HmacSHA256, 300, time.Now().Unix())

	// c := new(dns.Client)
	c := dns.Client{Net: "tcp"}
	c.TsigSecret = map[string]string{tsig[0] + ".": tsig[1]}
	r, _, err := c.Exchange(m, signer.Address+":53") // TODO: add DnsAddress or solve this in a better way
	if err != nil {
		fmt.Printf("RLDdnsFetchRRset: Error from Exchange: %v. Returning response chan + call stack\n", err)
		fdop.Response <- SignerOpResult{ Error: err}
		// fmt.Printf("RLDdnsFetchRRset: post response chan after exchange error\n", err)
		return false, 0, nil
	}

	if r.MsgHdr.Rcode != dns.RcodeSuccess {
		err = fmt.Errorf("Fetch of %s RRset failed, RCODE = %s", dns.TypeToString[rrtype],
		      			      	    	    	    	 dns.RcodeToString[r.MsgHdr.Rcode])
		fmt.Printf("RLDdnsFetchRRset: Rcode error: %v. Returning response chan + call stack\n", err)
		fdop.Response <- SignerOpResult{Error: err}
		// fmt.Printf("RLDdnsFetchRRset: post response chan after rcode error\n", err)
		return false, 0, nil
	}

	log.Printf("RLDDNS: Length of Answer from %s: %d RRs\n", signer.Name, len(r.Answer))

	var rrs []dns.RR

	// XXX: Here we want to filter out all RRs that are of other types than the
	//      rrtype we're looking for. It would be much better to have a general
	//      check for a.(type) == rrtype, but I have not figured out how.

	for _, a := range r.Answer {
		switch dns.TypeToString[rrtype] {

		case "DNSKEY":
			rr, ok := a.(*dns.DNSKEY)
			if !ok {
				continue
			}
			rrs = append(rrs, rr)

		case "CDS":
			rr, ok := a.(*dns.CDS)
			if !ok {
				continue
			}
			rrs = append(rrs, rr)

		case "CDNSKEY":
			rr, ok := a.(*dns.CDNSKEY)
			if !ok {
				continue
			}
			rrs = append(rrs, rr)

		case "NS":
			rr, ok := a.(*dns.NS)
			if !ok {
				continue
			}
			rrs = append(rrs, rr)

		case "DS":
			rr, ok := a.(*dns.DS)
			if !ok {
				continue
			}
			rrs = append(rrs, rr)

		case "SOA":
			rr, ok := a.(*dns.SOA)
			if !ok {
				continue
			}
			rrs = append(rrs, rr)

		case "CSYNC":
			rr, ok := a.(*dns.CSYNC)
			if !ok {
				continue
			}
			rrs = append(rrs, rr)

		}
	}

	// fmt.Printf("RLDdnsFetchRRset: All ok. Returning result ->response chan + call stack\n", err)
	fdop.Response <- SignerOpResult{
		Status:   0, // should perhaps use DNS Rcodes?
		RRs:      rrs,
		Error:    nil,
		Response: "Tjolahopp",
	}
	// fmt.Printf("RLDdnsFetchRRset: post response chan\n", err)

	return false, 0, nil
}
