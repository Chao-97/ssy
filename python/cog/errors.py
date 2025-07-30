class CogError(Exception):
    """Base class for all Cog errors."""


class ConfigDoesNotExist(CogError):
    """Exception raised when a ssy.yaml does not exist."""


class PredictorNotSet(CogError):
    """Exception raised when 'predict' is not set in ssy.yaml when it needs to be."""
