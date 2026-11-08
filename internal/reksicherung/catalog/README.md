# Airon inventory

`catalog.FromConfig` validates the configured backup catalog and emits a stable
inventory ordered by source ID. `Inventory.Marshal` produces compact JSON that
can be supplied as an Airon string input (for example, `inventory`).

The inventory contains only the metadata needed to select and describe a
restore-check target. It deliberately does not include secret references,
resolved credentials, storage endpoints, restore image settings, or resource
limits. ReKSicherung remains responsible for resolving operational details by
the stable source ID; prompts should not need storage credentials or container
configuration.
