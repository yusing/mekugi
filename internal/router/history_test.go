package router

import (
	"context"

	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func testHistory() *history.Service {
	service, err := history.NewService(RetainedRecordKinds())
	if err != nil {
		panic(err)
	}
	return service
}

func openMekugiReplayStore(directory string) (*mekugiReplayStore, error) {
	return openReplayStore(context.Background(), testHistory(), directory)
}

func testReplayStore(store mekugiReplayStore) *mekugiReplayStore {
	store.publication = testHistory().Attach(store.directory)
	return &store
}
