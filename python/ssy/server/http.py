# SSY HTTP server - wrapper for cog.server.http

import os
import sys

# Add the parent directory to sys.path to find cog
_current_dir = os.path.dirname(os.path.abspath(__file__))
_parent_dir = os.path.dirname(os.path.dirname(_current_dir))
if _parent_dir not in sys.path:
    sys.path.insert(0, _parent_dir)

# If this is run as main module, execute the original cog.server.http
if __name__ == "__main__":
    # Replace argv to change the displayed name in version output
    original_argv = sys.argv[:]

    # Check if --version is requested to customize output
    if "--version" in sys.argv or "-v" in sys.argv:
        try:
            from cog._version import __version__
        except ImportError:
            __version__ = "dev"
        print(f"ssy.server.http {__version__}")
        sys.exit(0)

    # For non-version requests, run the original module
    import runpy

    runpy.run_module("cog.server.http", run_name="__main__")
else:
    # Re-export everything from cog.server.http for import purposes
    from cog.server.http import *  # noqa: F401, F403
