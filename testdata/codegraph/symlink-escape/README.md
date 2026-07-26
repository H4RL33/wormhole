This fixture documents the lifecycle rejection case for a Git-tracked Go path
whose working-tree entry is replaced by a symlink outside the canonical
checkout. Tests create the platform-specific symlink at runtime so the fixture
remains portable and does not dereference repository-external bytes.
