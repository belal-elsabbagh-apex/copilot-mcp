package uipath

import "testing"

func TestParseQueueItemURL(t *testing.T) {
	const u = "https://cloud.uipath.com/myorg/mytenant/orchestrator_/queues/38592/transactions(sidepanel:sidepanel/transactions/7/details)?fid=434039"
	got, err := parseQueueItemURL(u)
	if err != nil || got != (parsedQueueURL{txnID: 7, folderID: "434039", queueID: 38592}) {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestParseQueueItemURLWithoutAQueueSegment(t *testing.T) {
	got, err := parseQueueItemURL("https://cloud.uipath.com/o/t/orchestrator_/x(sidepanel:sidepanel/transactions/9/details)?fid=1")
	if err != nil || got.queueID != 0 || got.txnID != 9 {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestParseQueueItemURLRejectsMissingParts(t *testing.T) {
	for name, u := range map[string]string{
		"no fid":         "https://cloud.uipath.com/o/t/orchestrator_/x/transactions/9/details",
		"no transaction": "https://cloud.uipath.com/o/t/orchestrator_/queues/1?fid=1",
		"not a url":      "not a url",
	} {
		if _, err := parseQueueItemURL(u); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
