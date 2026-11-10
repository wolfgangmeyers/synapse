# Background bash platform limitation

Status: Draft; supplements `background-bash.spec.md` without claiming its acceptance is complete.

## Unsupported launch

On non-Linux platforms, a background bash launch is rejected with an explicit unsupported-platform error before a shell is started or a job is created. Safe process-tree termination and timeout behavior have not been validated there. Linux launch behavior is unchanged. This restriction does not apply to foreground Bash.

Build-tagged Go tests cover the platform selection and the rejected launch path. Cross-compilation checks build compatibility only; it does not demonstrate runtime behavior on another operating system. The full background-bash acceptance matrix remains incomplete.
