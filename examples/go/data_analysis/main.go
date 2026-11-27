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

// Backend: agent-sandbox or OpenSandbox with Python.
// Run: go run ./examples/go/data_analysis
// Expected: Rows: 3, revenue: 42.00.
package main

import "github.com/e2bgateway/e2bgateway/examples/go/internal/example"

func main() {
	example.PythonMain(`
import csv
import io

sales = io.StringIO("item,price\nnotebook,12\npen,5\nbag,25\n")
rows = list(csv.DictReader(sales))
print(f"Rows: {len(rows)}, revenue: {sum(float(row['price']) for row in rows):.2f}")
`)
}
