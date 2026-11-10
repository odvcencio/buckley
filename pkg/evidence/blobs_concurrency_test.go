package evidence

import (
	"bytes"
	"fmt"
	"sync"
	"testing"
)

func TestBlobStore_ConcurrentStoresKeepIndependentFrames(t *testing.T) {
	const writers = 8
	stores := make([]*BlobStore, writers)
	for i := range stores {
		var err error
		stores[i], err = NewBlobStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for i, store := range stores {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for round := 0; round < 4; round++ {
				content := bytes.Repeat([]byte(fmt.Sprintf("store-%d round-%d evidence\n", i, round)), 16*1024)
				path, err := store.Write(ContentSHA256Hex(content), content)
				if err != nil {
					t.Error(err)
					return
				}
				got, err := store.Read(path)
				if err != nil {
					t.Error(err)
					return
				}
				if !bytes.Equal(got, content) {
					t.Error("shared compression worker mixed evidence frames")
					return
				}
			}
		}()
	}
	wg.Wait()
}
