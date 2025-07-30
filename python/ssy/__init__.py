# SSY - renamed from Cog for custom deployment
# This module re-exports all cog functionality under the ssy namespace

import os
import sys

# Add the parent directory to sys.path to find cog
_current_dir = os.path.dirname(os.path.abspath(__file__))
_parent_dir = os.path.dirname(_current_dir)
if _parent_dir not in sys.path:
    sys.path.insert(0, _parent_dir)

# Import version info first (must be after sys.path modification)
try:
    from cog._version import __version__, version, version_tuple  # noqa: E402

    # Note: cog._version doesn't have __version_tuple__, only version_tuple
    __version_tuple__ = version_tuple
except ImportError:
    __version__ = "dev"
    __version_tuple__ = (0, 0, 0, "dev")
    version = __version__
    version_tuple = __version_tuple__

# Import core cog components individually to avoid wildcard import issues
from cog import BaseModel  # noqa: F401, E402
from cog.base_predictor import BasePredictor  # noqa: F401, E402
from cog.server.scope import current_scope  # noqa: F401, E402
from cog.types import (
    AsyncConcatenateIterator,  # noqa: F401, E402
    ConcatenateIterator,
    ExperimentalFeatureWarning,
    File,
    Input,
    Path,
    Secret,
)

# Additional exports that might be used directly
try:
    from cog.mode import Mode  # noqa: F401, E402
except ImportError:
    Mode = None

try:
    from cog.schema import Status, WebhookEvent  # noqa: F401, E402
except ImportError:
    Status = None
    WebhookEvent = None

try:
    from cog.config import Config  # noqa: F401, E402
except ImportError:
    Config = None

# Provide SsyPath as an alias for Path
SsyPath = Path

# Define what gets exported when using "from ssy import *"
__all__ = [
    "__version__",
    "__version_tuple__",
    "version",
    "version_tuple",
    "BasePredictor",
    "Input",
    "Path",
    "SsyPath",  # Alias for Path
    "File",
    "Secret",
    "BaseModel",
    "ConcatenateIterator",
    "AsyncConcatenateIterator",
    "current_scope",
    "ExperimentalFeatureWarning",
    # Additional potentially useful exports (if available)
    "Mode",
    "Status",
    "WebhookEvent",
    "Config",
]
