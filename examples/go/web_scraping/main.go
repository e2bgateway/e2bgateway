// Copyright The E2BGateway Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Backend: agent-sandbox or OpenSandbox with Python and HTTPS egress.
// Run: go run ./examples/go/web_scraping
// Expected: The Example Domain page title.
package main

import "github.com/e2bgateway/e2bgateway/examples/go/internal/example"

func main() {
	example.PythonMain(`
from html.parser import HTMLParser
from urllib.request import urlopen

class TitleParser(HTMLParser):
    in_title = False
    title = ""

    def handle_starttag(self, tag, attrs):
        if tag == "title":
            self.in_title = True

    def handle_endtag(self, tag):
        if tag == "title":
            self.in_title = False

    def handle_data(self, data):
        if self.in_title:
            self.title += data

with urlopen("https://example.com/", timeout=10) as page:
    parser = TitleParser()
    parser.feed(page.read(65536).decode("utf-8"))
    print(f"Title: {parser.title.strip()}")
`)
}
