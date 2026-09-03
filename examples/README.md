# Examples

| Example | Demonstrates |
| --- | --- |
| [basic-api](basic-api/README.md) | An independent Go module using a copied server block |

Examples use separate Go modules to catch accidental dependencies on the
development repository. Check scripts discover and verify every module with
`GOWORK=off`. PostgreSQL and Redis are only needed when a feature uses them.
