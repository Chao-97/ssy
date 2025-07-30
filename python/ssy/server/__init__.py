# SSY server module - re-exports cog.server functionality

import os
import sys

# Add the parent directory to sys.path to find cog
_current_dir = os.path.dirname(os.path.abspath(__file__))
_parent_dir = os.path.dirname(os.path.dirname(_current_dir))
if _parent_dir not in sys.path:
    sys.path.insert(0, _parent_dir)

# Re-export everything from cog.server
from cog.server import *  # noqa: F401, F403
