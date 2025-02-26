/*
Maddy Mail Server - Composable all-in-one email server.
Copyright © 2019-2020 Max Mazurov <fox.cpp@disroot.org>, Maddy Mail Server contributors

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package table

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/foxcpp/maddy/framework/config"
	"github.com/foxcpp/maddy/framework/hooks"
	"github.com/foxcpp/maddy/framework/log"
	"github.com/foxcpp/maddy/framework/module"
)

const FileModName = "table.file"

type File struct {
	instName string
	file     string

	m      map[string][]string
	mLck   sync.RWMutex
	mStamp time.Time

	stopReloader chan struct{}
	forceReload  chan struct{}

	log log.Logger
}

func NewFile(_, instName string, _, inlineArgs []string) (module.Module, error) {
	m := &File{
		instName:     instName,
		m:            make(map[string][]string),
		stopReloader: make(chan struct{}),
		forceReload:  make(chan struct{}),
		log:          log.Logger{Name: FileModName},
	}

	switch len(inlineArgs) {
	case 1:
		m.file = inlineArgs[0]
	case 0:
	default:
		return nil, fmt.Errorf("%s: cannot use multiple files with single %s, use %s multiple times to do so", FileModName, FileModName, FileModName)
	}

	return m, nil
}

func (f *File) Name() string {
	return FileModName
}

func (f *File) InstanceName() string {
	return f.instName
}

func (f *File) Init(cfg *config.Map) error {
	var file string
	cfg.Bool("debug", true, false, &f.log.Debug)
	cfg.String("file", false, false, "", &file)
	if _, err := cfg.Process(); err != nil {
		return err
	}

	if file != "" {
		if f.file != "" {
			return fmt.Errorf("%s: file path specified both in directive and in argument, do it once", FileModName)
		}
		f.file = file
	}

	if err := readFile(f.file, f.m); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		f.log.Printf("ignoring non-existent file: %s", f.file)
	}

	go f.reloader()
	hooks.AddHook(hooks.EventReload, func() {
		f.forceReload <- struct{}{}
	})

	return nil
}

var reloadInterval = 15 * time.Second

func (f *File) reloader() {
	defer func() {
		if err := recover(); err != nil {
			stack := debug.Stack()
			log.Printf("panic during m reload: %v\n%s", err, stack)
		}
	}()

	t := time.NewTicker(reloadInterval)

	for {
		select {
		case <-t.C:
			var latestStamp time.Time
			info, err := os.Stat(f.file)
			if err != nil {
				if os.IsNotExist(err) {
					f.mLck.Lock()
					f.m = map[string][]string{}
					f.mStamp = time.Now()
					f.mLck.Unlock()
					continue
				}
				f.log.Printf("%v", err)
			}
			if info.ModTime().After(latestStamp) {
				latestStamp = info.ModTime()
			}
		case <-f.forceReload:
		case <-f.stopReloader:
			f.stopReloader <- struct{}{}
			return
		}

		f.log.Debugf("reloading")

		newm := make(map[string][]string, len(f.m)+5)
		if err := readFile(f.file, newm); err != nil {
			if os.IsNotExist(err) {
				f.log.Printf("ignoring non-existent file: %s", f.file)
				continue
			}

			f.log.Println(err)
			continue
		}

		f.mLck.Lock()
		f.m = newm
		f.mStamp = time.Now()
		f.mLck.Unlock()
	}
}

func (f *File) Close() error {
	f.stopReloader <- struct{}{}
	<-f.stopReloader
	return nil
}

func readFile(path string, out map[string][]string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}

	scnr := bufio.NewScanner(f)
	lineCounter := 0

	parseErr := func(text string) error {
		return fmt.Errorf("%s:%d: %s", path, lineCounter, text)
	}

	for scnr.Scan() {
		lineCounter++
		if strings.HasPrefix(scnr.Text(), "#") {
			continue
		}

		text := strings.TrimSpace(scnr.Text())
		if text == "" {
			continue
		}

		parts := strings.SplitN(text, ":", 2)
		if len(parts) == 1 {
			parts = append(parts, "")
		}

		from := strings.TrimSpace(parts[0])
		if len(from) == 0 {
			return parseErr("empty address before colon")
		}
		to := strings.TrimSpace(parts[1])

		out[from] = append(out[from], to)
	}
	return scnr.Err()
}

func (f *File) Lookup(_ context.Context, val string) (string, bool, error) {
	// The existing map is never modified, instead it is replaced with a new
	// one if reload is performed.
	f.mLck.RLock()
	usedFile := f.m
	f.mLck.RUnlock()

	newVal, ok := usedFile[val]

	if len(newVal) == 0 {
		return "", false, nil
	}

	return newVal[0], ok, nil
}

func (f *File) LookupMulti(_ context.Context, val string) ([]string, error) {
	f.mLck.RLock()
	usedFile := f.m
	f.mLck.RUnlock()

	return usedFile[val], nil
}

func init() {
	module.Register(FileModName, NewFile)
}
