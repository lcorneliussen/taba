Secret/control roots now belong to the receiving project or standalone collection.
Inherited or generated root variables cannot authorize another source; external
sharing requires an exact receiving configuration declaration. Standalone
collections default to their own `.config`, and retirement preserves nonempty
private control data. Read-only project/location metadata supports explicit
project and collection boundaries without selecting an existing task. These
checks do not claim provider credential isolation or change global stores.
