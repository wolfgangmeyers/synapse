# Native operational tools implementation plan

1. Add one operational tool module with the grouped tool definitions, closed action schemas, runtime validation, fixed script allowlist, argument builders, typed bounded output, credential redaction, and injectable runner and router adapters.
2. Register the module once from `createMechaMessagingExtension` and pass adapter injection through one options object.
3. Extend the installer with an explicit Python and JavaScript dependency closure, fixed target topology, source validation, and whole-bundle rollback.
4. Add focused Node tests for registration, schema shape, runtime validation, safe script arguments, router isolation, typed output, bounds, redaction, and ordinary identifier preservation.
5. Add installer and standalone tests for closure rejection, rollback, clean-target imports, and actual installed execution through all eight tools.
6. Run only the focused headless Node tests, installer unit tests, static checks, and diff checks.

## Non-goals

This change does not add arbitrary command execution, arbitrary repository or path selection, worker launch by caller-supplied directory, or changes to existing task specifications.
